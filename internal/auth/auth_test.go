package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestKeysAreKeptOutsideTheTeam(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TUBAS_DATA_DIR", dir) // tests never touch the real keychain
	where, err := Set("deepseek", "sk-123")
	if err != nil || where != "file" {
		t.Fatalf("Set: %q %v", where, err)
	}
	if Get("deepseek") != "sk-123" || Get("kimi") != "" {
		t.Errorf("Get: %q", Get("deepseek"))
	}
	if c := Connected(); c["deepseek"] != "file" || len(c) != 1 {
		t.Errorf("Connected: %v", c)
	}
	info, err := os.Stat(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("only you can read it: %v", info.Mode().Perm())
	}
	if !strings.HasSuffix(Where("file"), "auth.json") {
		t.Errorf("Where: %s", Where("file"))
	}
	if err := Delete("deepseek"); err != nil || Get("deepseek") != "" || len(Connected()) != 0 {
		t.Errorf("Delete: %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".auth-*"))
	if len(leftovers) > 0 {
		t.Errorf("no temporary files left: %v", leftovers)
	}
}

func TestYourOwnServers(t *testing.T) {
	t.Setenv("TUBAS_DATA_DIR", t.TempDir())
	if err := AddCustom("my-gpu", Custom{OpenAI: "http://my-gpu:8000/v1", Models: []string{"qwen3-coder"}}); err != nil {
		t.Fatal(err)
	}
	Set("my-gpu", "local-key")
	if c := Customs()["my-gpu"]; c.OpenAI != "http://my-gpu:8000/v1" || c.Models[0] != "qwen3-coder" {
		t.Errorf("Customs: %+v", c)
	}
	if err := RemoveCustom("my-gpu"); err != nil || len(Customs()) != 0 || Get("my-gpu") != "" {
		t.Errorf("RemoveCustom takes its key too: %v", err)
	}
}

// The real keychain, when asked for: TUBAS_REAL_KEYCHAIN=1 go test.
func TestTheRealKeychain(t *testing.T) {
	if os.Getenv("TUBAS_REAL_KEYCHAIN") != "1" {
		t.Skip("touches this computer's keychain: set TUBAS_REAL_KEYCHAIN=1")
	}
	dir := t.TempDir()
	t.Setenv("TUBAS_DATA_DIR", dir)
	t.Setenv("TUBAS_KEYCHAIN", "on")
	where, err := Set("tubas-selftest", "throwaway-123")
	if err != nil || where != "keychain" {
		t.Fatalf("Set: %q %v", where, err)
	}
	defer Delete("tubas-selftest")
	data, _ := os.ReadFile(filepath.Join(dir, "auth.json"))
	if strings.Contains(string(data), "throwaway") {
		t.Error("in the keychain, the key isn't in the file")
	}
	if Get("tubas-selftest") != "throwaway-123" {
		t.Error("read back from the keychain")
	}
	if err := Delete("tubas-selftest"); err != nil || Get("tubas-selftest") != "" {
		t.Errorf("deleted: %v", err)
	}
}
