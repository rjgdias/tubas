package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"tubas/internal/auth"
	"tubas/internal/llm"
)

// Connection is how tubas reaches one provider: for /connect, the first
// screen and doctor.
type Connection struct {
	Name   string
	State  string // ConnGateway, ConnEnv, ConnSaved, ConnFree or ConnMissing
	Detail string // where the key comes from, for people
	Custom bool   // a provider you added by its address
}

// What a provider's key is.
const (
	ConnGateway = "gateway" // your Claude settings point at a company gateway
	ConnEnv     = "env"     // an environment variable
	ConnSaved   = "saved"   // saved with /connect
	ConnFree    = "free"    // needs no key (a model on this computer)
	ConnMissing = "missing"
)

// Ready says whether the provider can be used now.
func (c Connection) Ready() bool { return c.State != ConnMissing }

// Connections is every provider and how tubas reaches it, anthropic first.
// setup is your Claude settings (FindProvider); its apiKeyHelper isn't run.
func Connections(setup Provider, providers map[string]ProviderSpec) []Connection {
	saved := auth.Connected()
	customs := auth.Customs()
	out := []Connection{anthropicConnection(setup, saved)}
	names := make([]string, 0, len(providers))
	for name := range providers {
		if name != Anthropic {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		spec := providers[name]
		c := Connection{Name: name, State: ConnMissing}
		_, c.Custom = customs[name]
		switch {
		case spec.KeyEnv != "" && os.Getenv(spec.KeyEnv) != "":
			c.State, c.Detail = ConnEnv, spec.KeyEnv
		case saved[name] != "":
			c.State, c.Detail = ConnSaved, auth.Where(saved[name])
		case spec.KeyEnv == "":
			c.State, c.Detail = ConnFree, spec.OpenAI
		default:
			c.Detail = spec.KeyEnv
		}
		out = append(out, c)
	}
	return out
}

func anthropicConnection(setup Provider, saved map[string]string) Connection {
	c := Connection{Name: Anthropic, State: ConnMissing, Detail: "ANTHROPIC_API_KEY"}
	switch base := setup.Get("ANTHROPIC_BASE_URL"); {
	case base != "" && base != anthropicAPI.Anthropic:
		c.State, c.Detail = ConnGateway, "company gateway "+base
		if setup.KeyHelper != "" {
			c.Detail += " · apiKeyHelper"
		}
	case setup.KeyHelper != "":
		c.State, c.Detail = ConnGateway, "apiKeyHelper in your Claude settings"
	case setup.Get("ANTHROPIC_API_KEY") != "":
		c.State, c.Detail = ConnEnv, "ANTHROPIC_API_KEY"
	case setup.Get("ANTHROPIC_AUTH_TOKEN") != "":
		c.State, c.Detail = ConnEnv, "ANTHROPIC_AUTH_TOKEN"
	case saved[Anthropic] != "":
		c.State, c.Detail = ConnSaved, auth.Where(saved[Anthropic])
	}
	return c
}

// ErrKeyRefused is a key the provider said no to.
var ErrKeyRefused = errors.New("the provider refused this key")

// CheckKey asks the provider for its models with key: a key that works
// lists them. A refused key is ErrKeyRefused; any other failure (no model
// list at that address, the network) is returned as it is, and the key
// may still be good.
func CheckKey(ctx context.Context, spec ProviderSpec, key string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if spec.Name == Anthropic && spec.Anthropic == "" {
		spec.Anthropic = anthropicAPI.Anthropic
	}
	var lister llm.Lister
	switch {
	case spec.OpenAI != "":
		lister = &llm.OpenAI{Base: spec.OpenAI, Key: key}
	case spec.Anthropic != "":
		lister = &llm.Anthropic{Base: spec.Anthropic, Key: key, Official: spec.Anthropic == anthropicAPI.Anthropic}
	default:
		return nil, fmt.Errorf("%s has no address to reach", spec.Name)
	}
	models, err := lister.Models(ctx)
	if s := llm.StatusOf(err); s == 401 || s == 403 {
		return nil, fmt.Errorf("%w (%d)", ErrKeyRefused, s)
	}
	return models, err
}

// Spec is a provider by name: the known ones, then the ones you added
// with /connect since tubas started.
func Spec(providers map[string]ProviderSpec, name string) (ProviderSpec, bool) {
	if name == Anthropic {
		return anthropicAPI, true
	}
	if spec, ok := providers[name]; ok {
		return spec, true
	}
	if c, ok := auth.Customs()[name]; ok {
		return ProviderSpec{Name: name, OpenAI: c.OpenAI, Models: c.Models}, true
	}
	return ProviderSpec{}, false
}
