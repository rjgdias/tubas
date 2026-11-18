package llm

import "strings"

// Caps is what a model accepts and what it costs.
type Caps struct {
	Adaptive  bool // thinking {type: "adaptive"}
	Effort    bool // output_config.effort
	MaxOutput int  // the max_tokens to ask for
	Context   int  // tokens it reads at most
	Price     Price
	Known     bool // from the table, not a guess
}

// Price is US$ per million tokens.
type Price struct {
	Input, Output, CacheRead, CacheWrite float64
}

// Cost is what a usage costs at this price (0 when unknown).
func (p Price) Cost(u Usage) float64 {
	fresh := u.Input - u.CacheRead - u.CacheWrite
	if fresh < 0 {
		fresh = 0
	}
	return (float64(fresh)*p.Input + float64(u.CacheRead)*p.CacheRead +
		float64(u.CacheWrite)*p.CacheWrite + float64(u.Output)*p.Output) / 1e6
}

// Anthropic's current models (the claude-api skill's table, Nov 2026).
// A cache write costs 1.25 times the input; a read, a tenth or less.
var claudeModels = map[string]Caps{
	"claude-opus-5-5":   {Adaptive: true, Effort: true, MaxOutput: 32000, Context: 1_000_000, Price: Price{4, 20, 0.20, 5}},
	"claude-sonnet-5-5": {Adaptive: true, Effort: true, MaxOutput: 32000, Context: 1_000_000, Price: Price{2, 10, 0.10, 2.5}},
	"claude-haiku-5-5":  {Adaptive: true, Effort: true, MaxOutput: 32000, Context: 1_000_000, Price: Price{0.10, 0.50, 0.01, 0.125}},
	"claude-fable-5-2":  {Adaptive: true, Effort: true, MaxOutput: 32000, Context: 1_000_000, Price: Price{8, 40, 0.20, 10}},
}

// Aliases are Claude's short model names.
var Aliases = map[string]string{"haiku": "claude-haiku-5-5", "sonnet": "claude-sonnet-5-5", "opus": "claude-opus-5-5"}

// IsClaude says whether a model name is Claude's (a short name or an id).
func IsClaude(model string) bool {
	_, alias := Aliases[model]
	return alias || strings.HasPrefix(model, "claude-") || strings.Contains(model, "claude")
}

// Lookup is what a model accepts. like is the Claude model it stands for
// when a gateway names it its own way (sonnet → acme-claude-sonnet): the
// gateway serves that model, so it accepts what that model accepts.
func Lookup(model, like string) Caps {
	for _, name := range []string{model, like, Aliases[like]} {
		if c, ok := claudeModels[name]; ok {
			c.Known = true
			return c
		}
	}
	for id, c := range claudeModels { // a dated or suffixed id: claude-sonnet-5-5-…
		if strings.HasPrefix(model, id) {
			c.Known = true
			return c
		}
	}
	return Caps{MaxOutput: 8192, Context: 128_000}
}
