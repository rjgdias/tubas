package engine

import "tubas/internal/agent"

// GatewayLaunch is how a harness starts the tubas tool gateway for one
// turn: an MCP server over stdio. Every harness gets the same gateway, so
// tools, access and approvals work the same on claude, codex, an ACP
// agent or the api harness (agentes.chat and tucano do the same).
type GatewayLaunch struct {
	Command string
	Args    []string
	Env     map[string]string
}

// GatewayFunc gives an agent its gateway for one turn. turn routes its
// approvals back; writeDirs are the worktrees it may change; harness
// tells the gateway which shell it needs (claude asks through the
// permission tool, the others get run_command). ok is false when the
// agent has no tools at all.
type GatewayFunc func(a agent.Agent, turn string, writeDirs []string, harness string) (launch GatewayLaunch, ok bool)
