package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Agents run with --setting-sources "", so Claude Code skips your
// settings files: no hooks, no CLAUDE.md, no plugins, no permissions.
// That also drops the "env" block of those files, which is where a work
// setup usually says which AI provider to use (a company gateway in
// ANTHROPIC_BASE_URL, Bedrock, Vertex, a proxy, a CA certificate). And
// --settings does not bring it back in that mode.
//
// So tubas reads those files itself and passes on only the variables
// that choose and reach the provider, through the process environment.
// Everything else in your settings stays out of the agents.

// Provider is how agents reach the model, as your Claude settings say.
type Provider struct {
	Env     map[string]string // variables to pass to claude
	Sources []string          // the settings files they came from

	// KeyHelper is your settings' apiKeyHelper: a command that prints the
	// key (company setups often fetch a short-lived token this way).
	// Agents run without your settings, so tubas runs it for them.
	KeyHelper string
}

// Label describes the provider for the header: "" for Claude's own
// login, else the gateway's host, "bedrock", "vertex" or "foundry".
func (p Provider) Label() string {
	get := func(k string) string {
		if v, ok := p.Env[k]; ok {
			return v
		}
		return os.Getenv(k)
	}
	switch {
	case truthy(get("CLAUDE_CODE_USE_BEDROCK")):
		return "bedrock"
	case truthy(get("CLAUDE_CODE_USE_VERTEX")):
		return "vertex"
	case truthy(get("CLAUDE_CODE_USE_FOUNDRY")):
		return "foundry"
	}
	if base := get("ANTHROPIC_BASE_URL"); base != "" {
		if u, err := url.Parse(base); err == nil && u.Host != "" {
			if u.Hostname() == "api.anthropic.com" {
				return "" // the official API is not a gateway
			}
			return "gateway " + u.Host
		}
		return "gateway"
	}
	return ""
}

// Environ is the environment for a claude process: this process's own,
// with the provider's variables on top (your settings win, as they do
// when you run claude yourself).
func (p Provider) Environ() []string {
	if len(p.Env) == 0 {
		return nil // nil means: inherit this process's environment
	}
	env := make([]string, 0, len(os.Environ())+len(p.Env))
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if _, replaced := p.Env[k]; !replaced {
			env = append(env, kv)
		}
	}
	keys := make([]string, 0, len(p.Env))
	for k := range p.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if p.Env[k] == "" {
			continue // an empty value removes the variable
		}
		env = append(env, k+"="+p.Env[k])
	}
	return env
}

// With is p with other's variables on top.
func (p Provider) With(other Provider) Provider {
	env := make(map[string]string, len(p.Env)+len(other.Env))
	for k, v := range p.Env {
		env[k] = v
	}
	for k, v := range other.Env {
		env[k] = v
	}
	return Provider{Env: env, Sources: p.Sources, KeyHelper: p.KeyHelper}
}

// Get is a variable as the agents will see it: from the provider, else
// from this process.
func (p Provider) Get(k string) string {
	if v, ok := p.Env[k]; ok {
		return v
	}
	return os.Getenv(k)
}

// Resolved runs the apiKeyHelper, when there is one, and puts the key
// where Claude Code would send it: as X-Api-Key and as a bearer token.
func (p Provider) Resolved() (Provider, error) {
	if p.KeyHelper == "" {
		return p, nil
	}
	key, err := helperKey(p.KeyHelper, p.Get("CLAUDE_CODE_API_KEY_HELPER_TTL_MS"))
	if err != nil {
		return p, fmt.Errorf("apiKeyHelper from your Claude settings: %w", err)
	}
	return p.With(Provider{Env: map[string]string{"ANTHROPIC_API_KEY": key, "ANTHROPIC_AUTH_TOKEN": key}}), nil
}

// helperKey runs a key helper, reusing its answer for a while (5 minutes,
// or CLAUDE_CODE_API_KEY_HELPER_TTL_MS), as Claude Code does.
var helper struct {
	sync.Mutex
	cmd, key string
	at       time.Time
}

func helperKey(command, ttlMS string) (string, error) {
	ttl := 5 * time.Minute
	if ms, err := strconv.Atoi(strings.TrimSpace(ttlMS)); err == nil && ms > 0 {
		ttl = time.Duration(ms) * time.Millisecond
	}
	helper.Lock()
	defer helper.Unlock()
	if helper.cmd == command && helper.key != "" && time.Since(helper.at) < ttl {
		return helper.key, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := shellCommand(ctx, command).Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", command, err)
	}
	key := strings.TrimSpace(string(out))
	if key == "" {
		return "", fmt.Errorf("%s printed nothing", command)
	}
	helper.cmd, helper.key, helper.at = command, key, time.Now()
	return key, nil
}

// shellCommand runs a command line the way Claude Code runs hooks and
// helpers: in sh, or in Git Bash on Windows (PowerShell when there is none).
func shellCommand(ctx context.Context, line string) *exec.Cmd {
	if runtime.GOOS != "windows" {
		return exec.CommandContext(ctx, "/bin/sh", "-c", line)
	}
	for _, bash := range []string{os.Getenv("CLAUDE_CODE_GIT_BASH_PATH"), `C:\Program Files\Git\bin\bash.exe`} {
		if bash != "" {
			if _, err := os.Stat(bash); err == nil {
				return exec.CommandContext(ctx, bash, "-c", line)
			}
		}
	}
	return exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", line)
}

// Headers are ANTHROPIC_CUSTOM_HEADERS ("Name: value", one per line).
func (p Provider) Headers() map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(p.Get("ANTHROPIC_CUSTOM_HEADERS"), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// FindProvider reads the settings files Claude Code would read for a
// session in workDir, in its order (later files win): your user
// settings, then the project's shared and local settings.
func FindProvider(workDir string) Provider {
	p := Provider{Env: map[string]string{}}
	for _, path := range settingsFiles(workDir) {
		env, keyHelper, ok := readSettingsEnv(path)
		if !ok {
			continue
		}
		if keyHelper != "" {
			p.KeyHelper = keyHelper
			if len(p.Sources) == 0 || p.Sources[len(p.Sources)-1] != path {
				p.Sources = append(p.Sources, path)
			}
		}
		used := false
		for k, v := range env {
			if isProviderVar(k) {
				p.Env[k] = v
				used = true
			}
		}
		if used {
			p.Sources = append(p.Sources, path)
		}
	}
	return p
}

func settingsFiles(workDir string) []string {
	var files []string
	configDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if configDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			configDir = filepath.Join(home, ".claude")
		}
	}
	if configDir != "" {
		files = append(files, filepath.Join(configDir, "settings.json"))
	}
	if workDir != "" {
		files = append(files,
			filepath.Join(workDir, ".claude", "settings.json"),
			filepath.Join(workDir, ".claude", "settings.local.json"))
	}
	return files
}

// readSettingsEnv returns the "env" block and the apiKeyHelper of one
// settings file. A file that is missing or unreadable is skipped, as
// Claude Code skips it.
func readSettingsEnv(path string) (map[string]string, string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", false
	}
	var s struct {
		Env          map[string]any `json:"env"`
		APIKeyHelper string         `json:"apiKeyHelper"`
	}
	if err := json.Unmarshal(data, &s); err != nil || (len(s.Env) == 0 && s.APIKeyHelper == "") {
		return nil, "", false
	}
	env := make(map[string]string, len(s.Env))
	for k, v := range s.Env {
		switch v := v.(type) {
		case string:
			env[k] = v
		case bool, float64:
			b, _ := json.Marshal(v)
			env[k] = string(b)
		}
	}
	return env, strings.TrimSpace(s.APIKeyHelper), true
}

// isProviderVar says whether a variable chooses or reaches the AI
// provider. Only these leave your settings for the agents.
func isProviderVar(name string) bool {
	for _, prefix := range []string{
		"ANTHROPIC_",        // gateway URL, token, key, headers, model names, vertex/bedrock URLs
		"CLAUDE_CODE_USE_",  // bedrock, vertex, foundry
		"CLAUDE_CODE_SKIP_", // skip bedrock/vertex auth behind a gateway
		"AWS_",              // bedrock region, profile, bearer token
		"CLOUD_ML_",         // vertex region
		"VERTEX_REGION_",    // vertex region per model
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	switch name {
	case "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
		"NODE_EXTRA_CA_CERTS", // a company CA for the gateway's certificate
		"CLAUDE_CODE_CLIENT_CERT", "CLAUDE_CODE_CLIENT_KEY", "CLAUDE_CODE_CLIENT_KEY_PASSPHRASE",
		"GOOGLE_APPLICATION_CREDENTIALS":
		return true
	}
	return false
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
