package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"tubas/internal/auth"
)

func TestASavedKeyIsUsedWhenTheEnvironmentHasNone(t *testing.T) {
	t.Setenv("TUBAS_DATA_DIR", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "")
	spec := Providers(nil)["deepseek"]
	if _, missing := spec.Key(); !missing {
		t.Fatal("no key yet")
	}
	auth.Set("deepseek", "saved-key")
	if key, missing := spec.Key(); missing || key != "saved-key" {
		t.Errorf("saved: %q %v", key, missing)
	}
	t.Setenv("DEEPSEEK_API_KEY", "env-key")
	if key, _ := spec.Key(); key != "env-key" {
		t.Errorf("the environment wins: %q", key)
	}
}

func TestConnectionsSayWhereEachKeyComesFrom(t *testing.T) {
	t.Setenv("TUBAS_DATA_DIR", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "x")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	auth.Set("deepseek", "k")
	auth.AddCustom("my-gpu", auth.Custom{OpenAI: "http://my-gpu/v1", Models: []string{"m"}})
	got := map[string]Connection{}
	for _, c := range Connections(Provider{}, Providers(nil)) {
		got[c.Name] = c
	}
	for name, want := range map[string]string{"anthropic": ConnMissing, "openai": ConnEnv, "deepseek": ConnSaved, "ollama": ConnFree, "kimi": ConnMissing, "my-gpu": ConnFree} {
		if got[name].State != want {
			t.Errorf("%s: %s, want %s (%s)", name, got[name].State, want, got[name].Detail)
		}
	}
	if !got["my-gpu"].Custom {
		t.Error("your own server is marked as yours")
	}
	gw := Connections(Provider{Env: map[string]string{"ANTHROPIC_BASE_URL": "https://gw.acme"}, KeyHelper: "get-token"}, nil)[0]
	if gw.State != ConnGateway || gw.Detail != "company gateway https://gw.acme · apiKeyHelper" {
		t.Errorf("the gateway: %+v", gw)
	}
}

func TestCheckingAKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer good":
			fmt.Fprint(w, `{"data":[{"id":"m1"},{"id":"m2"}]}`)
		default:
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"invalid key"}}`)
		}
	}))
	defer srv.Close()
	spec := ProviderSpec{Name: "acme", OpenAI: srv.URL}
	if models, err := CheckKey(context.Background(), spec, "good"); err != nil || len(models) != 2 {
		t.Errorf("a good key: %v %v", models, err)
	}
	if _, err := CheckKey(context.Background(), spec, "bad"); !errors.Is(err, ErrKeyRefused) {
		t.Errorf("a bad key: %v", err)
	}
	anth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"acme-sonnet","type":"model"}],"has_more":false}`)
	}))
	defer anth.Close()
	if models, err := CheckKey(context.Background(), ProviderSpec{Name: "gw", Anthropic: anth.URL}, "tok"); err != nil || len(models) != 1 || models[0] != "acme-sonnet" {
		t.Errorf("an Anthropic-style endpoint: %v %v", models, err)
	}
}
