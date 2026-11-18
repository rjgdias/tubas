package engine

import "tubas/internal/agent"

// GatewayLaunch is how a turn starts the tubas gateway for the team's
// connectors (data, reports, the workspace, MCP servers): an MCP server
// over stdio, which checks each agent's access and asks you before a
// write (agentes.chat and tucano do the same).
type GatewayLaunch struct {
	Command string
	Args    []string
	Env     map[string]string
}

// GatewayFunc gives an agent its gateway for one turn. turn routes its
// approvals back; writeDirs are the worktrees it may change. ok is false
// when the agent reaches no connector at all.
type GatewayFunc func(a agent.Agent, turn string, writeDirs []string) (launch GatewayLaunch, ok bool)
