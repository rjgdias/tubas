package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// ── what a work computer needs, copied from tucano ──────────────────────

// GatewayModels asks the gateway in your Claude settings which models it
// serves (GET <base>/v1/models), so the agent form can offer their real
// names. Nil when there is no gateway or it does not say.
func GatewayModels(ctx context.Context, p Provider) []string {
	base := p.Get("ANTHROPIC_BASE_URL")
	if base == "" {
		return nil
	}
	resolved, err := p.Resolved()
	if err != nil {
		return nil
	}
	key := resolved.Get("ANTHROPIC_AUTH_TOKEN")
	if key == "" {
		key = resolved.Get("ANTHROPIC_API_KEY")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+"/v1/models?limit=1000", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("anthropic-version", "2023-06-01")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("x-api-key", key)
	}
	for k, v := range p.Headers() {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&list) != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range list.Data {
		if m.ID != "" && !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m.ID)
		}
	}
	sort.Strings(out)
	return out
}

// AdoptPath makes agent CLIs findable where their installers put them,
// even when this process started without your shell's PATH (a desktop
// launcher, a service): the usual install folders, and on Linux and
// macOS the PATH of your login shell.
func AdoptPath() {
	home, _ := os.UserHomeDir()
	dirs := []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, ".opencode", "bin"),
		filepath.Join(home, ".bun", "bin"), filepath.Join(home, ".grok", "bin"), filepath.Join(home, "go", "bin")}
	if runtime.GOOS == "windows" {
		dirs = append(dirs, filepath.Join(os.Getenv("APPDATA"), "npm"), filepath.Join(os.Getenv("LOCALAPPDATA"), "cursor-agent"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "agy", "bin"))
	} else if _, err := exec.LookPath("claude"); err != nil {
		if shellPath := loginShellPath(); shellPath != "" {
			dirs = append(dirs, filepath.SplitList(shellPath)...)
		}
	}
	current := filepath.SplitList(os.Getenv("PATH"))
	have := map[string]bool{}
	for _, d := range current {
		have[filepath.Clean(d)] = true
	}
	for _, d := range dirs {
		if d == "" || have[filepath.Clean(d)] {
			continue
		}
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			current = append(current, d)
			have[filepath.Clean(d)] = true
		}
	}
	os.Setenv("PATH", strings.Join(current, string(os.PathListSeparator)))
}

// loginShellPath is the PATH your login shell sets up (nvm, asdf, brew…).
func loginShellPath() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, shell, "-ilc", `printf "__PATH__%s" "$PATH"`).Output()
	if err != nil {
		return ""
	}
	_, path, ok := strings.Cut(string(out), "__PATH__")
	if !ok {
		return ""
	}
	return strings.TrimSpace(path)
}

// friendlyHTTP says what a provider's error status means.
func friendlyHTTP(status int, body string) error {
	switch status {
	case 401, 403:
		return fmt.Errorf("the provider refused the key (%d): %s", status, body)
	case 404:
		return fmt.Errorf("not found (404): check the base URL and the model name: %s", body)
	case 429:
		return fmt.Errorf("rate limited (429): wait a moment and ask again: %s", body)
	}
	return fmt.Errorf("the provider answered %d: %s", status, body)
}
