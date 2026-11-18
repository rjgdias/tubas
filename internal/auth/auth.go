// Package auth keeps the keys you give tubas with /connect, as opencode
// does: in the system's keychain (Windows Credential Manager, the macOS
// Keychain, the Secret Service on Linux), or, where there is none, in a
// file only you can read, in your user folder. Never in a team folder or
// a repository, and never sent over the background server's API: the
// screen writes the key, the server reads it when a turn needs it.
//
// Which key wins: your Claude settings (a company gateway, its token or
// apiKeyHelper), then the provider's environment variable, then the key
// saved here.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"github.com/zalando/go-keyring"
)

const service = "tubas"

// Custom is a provider you added by its address: any OpenAI-compatible
// server. Its key, if it needs one, is kept like the others.
type Custom struct {
	OpenAI string   `json:"openai"`
	Models []string `json:"models"`
}

// file is auth.json: which providers are connected and where their key
// is, the keys themselves only when the keychain isn't there, and your
// own providers.
type file struct {
	Connected map[string]string `json:"connected,omitempty"` // provider → "keychain" or "file"
	Keys      map[string]string `json:"keys,omitempty"`
	Custom    map[string]Custom `json:"custom,omitempty"`
}

var mu sync.Mutex

// Dir is the folder tubas keeps your own things in: TUBAS_DATA_DIR, or
// your user configuration folder.
func Dir() (string, error) {
	if d := os.Getenv("TUBAS_DATA_DIR"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "tubas"), nil
}

// path is auth.json.
func path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "auth.json"), nil
}

// useKeychain says whether to try the system's keychain: not when
// TUBAS_KEYCHAIN=off, and not in tests (TUBAS_DATA_DIR) unless
// TUBAS_KEYCHAIN=on.
func useKeychain() bool {
	switch os.Getenv("TUBAS_KEYCHAIN") {
	case "off":
		return false
	case "on":
		return true
	}
	return os.Getenv("TUBAS_DATA_DIR") == ""
}

func read() (file, error) {
	var f file
	p, err := path()
	if err != nil {
		return f, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("%s is damaged: %w", p, err)
	}
	return f, nil
}

// write saves auth.json readable by you only, all at once: a crash never
// leaves half a file.
func write(f file) error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".auth-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Set saves a provider's key: in the keychain when there is one,
// otherwise in auth.json. It says where.
func Set(provider, key string) (where string, err error) {
	if provider == "" || key == "" {
		return "", errors.New("give the provider and its key")
	}
	mu.Lock()
	defer mu.Unlock()
	f, err := read()
	if err != nil {
		return "", err
	}
	if f.Connected == nil {
		f.Connected = map[string]string{}
	}
	if f.Keys == nil {
		f.Keys = map[string]string{}
	}
	where = "file"
	if useKeychain() && keyring.Set(service, provider, key) == nil {
		where = "keychain"
		delete(f.Keys, provider)
	} else {
		f.Keys[provider] = key
	}
	f.Connected[provider] = where
	return where, write(f)
}

// Get is the key saved for a provider, if any.
func Get(provider string) string {
	mu.Lock()
	defer mu.Unlock()
	f, err := read()
	if err != nil {
		return ""
	}
	switch f.Connected[provider] {
	case "keychain":
		if useKeychain() {
			if key, err := keyring.Get(service, provider); err == nil {
				return key
			}
		}
	case "file":
		return f.Keys[provider]
	}
	return ""
}

// Delete forgets a provider's key, wherever it was.
func Delete(provider string) error {
	mu.Lock()
	defer mu.Unlock()
	f, err := read()
	if err != nil {
		return err
	}
	if f.Connected[provider] == "keychain" && useKeychain() {
		if err := keyring.Delete(service, provider); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return err
		}
	}
	delete(f.Connected, provider)
	delete(f.Keys, provider)
	return write(f)
}

// Connected are the providers with a saved key, sorted, and where each
// key is.
func Connected() map[string]string {
	mu.Lock()
	defer mu.Unlock()
	f, _ := read()
	out := map[string]string{}
	for k, v := range f.Connected {
		out[k] = v
	}
	return out
}

// Where says, for people, where a key kept in place is.
func Where(place string) string {
	if place == "file" {
		p, _ := path()
		return p
	}
	switch runtime.GOOS {
	case "windows":
		return "Windows Credential Manager"
	case "darwin":
		return "the macOS Keychain"
	}
	return "the system keyring"
}

// AddCustom saves a provider of your own (its address and models; the
// key goes with Set).
func AddCustom(name string, c Custom) error {
	mu.Lock()
	defer mu.Unlock()
	f, err := read()
	if err != nil {
		return err
	}
	if f.Custom == nil {
		f.Custom = map[string]Custom{}
	}
	f.Custom[name] = c
	return write(f)
}

// RemoveCustom forgets a provider of your own, and its key.
func RemoveCustom(name string) error {
	mu.Lock()
	f, err := read()
	if err == nil {
		delete(f.Custom, name)
		err = write(f)
	}
	mu.Unlock()
	if err != nil {
		return err
	}
	return Delete(name)
}

// Customs are your own providers, by name.
func Customs() map[string]Custom {
	mu.Lock()
	defer mu.Unlock()
	f, _ := read()
	return f.Custom
}

// Names lists map keys, sorted.
func Names[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Destination says, for people, where the next key you save will go.
func Destination() string {
	if useKeychain() {
		return Where("keychain")
	}
	return Where("file")
}
