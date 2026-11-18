package mcp

import (
	"os"
	"strings"

	"tubas/internal/agent"
	"tubas/internal/engine"
)

// GatewayLauncher returns how a turn starts `tubas mcp` for its agent:
// the same binary, with the approval broker's address, the turn and the
// worktrees in its environment.
func GatewayLauncher(exe, teamDir, approvalURL, approvalToken string) engine.GatewayFunc {
	return func(a agent.Agent, turn string, writeDirs []string) (engine.GatewayLaunch, bool) {
		if !hasAccess(a) {
			return engine.GatewayLaunch{}, false
		}
		return engine.GatewayLaunch{
			Command: exe,
			Args:    []string{"mcp", "-team", teamDir, "-agent", a.Name},
			Env: map[string]string{"TUBAS_APPROVAL_URL": approvalURL, "TUBAS_APPROVAL_TOKEN": approvalToken, "TUBAS_TURN": turn,
				"TUBAS_WRITE_DIRS": strings.Join(writeDirs, string(os.PathListSeparator))},
		}, true
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
