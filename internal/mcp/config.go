package mcp

import (
	"encoding/json"
	"os"
	"strings"

	"tubas/internal/agent"
	"tubas/internal/engine"
)

// GatewayLauncher returns how any harness starts `tubas mcp` for one agent turn:
// the same binary, with the approval broker's address, the turn and the
// worktrees in its environment.
func GatewayLauncher(exe, teamDir, approvalURL, approvalToken string) engine.GatewayFunc {
	return func(a agent.Agent, turn string, writeDirs []string, harness string) (engine.GatewayLaunch, bool) {
		if !hasAccess(a) {
			return engine.GatewayLaunch{}, false
		}
		return engine.GatewayLaunch{
			Command: exe,
			Args:    []string{"mcp", "-team", teamDir, "-agent", a.Name},
			Env: map[string]string{"TUBAS_APPROVAL_URL": approvalURL, "TUBAS_APPROVAL_TOKEN": approvalToken, "TUBAS_TURN": turn,
				"TUBAS_WRITE_DIRS": strings.Join(writeDirs, string(os.PathListSeparator)), "TUBAS_HARNESS": harness},
		}, true
	}
}

// ClaudeTools returns the function ClaudeEngine calls before each turn.
// For an agent with any tool access, it writes a temporary MCP config
// telling claude to start `tubas mcp` for that agent, and allows the
// whole gateway: the gateway itself decides what the agent sees.
func ClaudeTools(exe, teamDir, approvalURL, approvalToken string) func(a agent.Agent, turn string, writeDirs []string) (string, []string, func(), error) {
	gateway := GatewayLauncher(exe, teamDir, approvalURL, approvalToken)
	return func(a agent.Agent, turn string, writeDirs []string) (string, []string, func(), error) {
		launch, ok := gateway(a, turn, writeDirs, "claude")
		if !ok {
			return "", nil, func() {}, nil
		}
		config := map[string]any{"mcpServers": map[string]any{ServerName: map[string]any{
			"type": "stdio", "command": launch.Command, "args": launch.Args, "env": launch.Env,
		}}}

		f, err := os.CreateTemp("", "tubas-mcp-*.json")
		if err != nil {
			return "", nil, nil, err
		}
		if err := json.NewEncoder(f).Encode(config); err != nil {
			f.Close()
			return "", nil, nil, err
		}
		f.Close()
		// "mcp__tubas" allows every tool of the server. That is safe because
		// the gateway only lists the tools this agent may use.
		return f.Name(), []string{"mcp__" + ServerName}, func() { os.Remove(f.Name()) }, nil
	}
}

func hasAccess(a agent.Agent) bool {
	if a.Escalate != nil && a.Escalate.Has("ask") {
		return true // ask_senior lives in the gateway
	}
	for _, access := range a.Tools {
		if access != "none" {
			return true
		}
	}
	return false
}
