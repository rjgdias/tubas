package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"tubas/internal/llm"
	"tubas/internal/tools"
)

// runCalls runs one step's tool calls and returns their results, in the
// order they were asked. When every call only reads files (read, glob,
// grep), they run at the same time.
func (e NativeEngine) runCalls(ctx context.Context, agentName string, calls []llm.Block, box *tools.Box, gw *rpcConn,
	real map[string]string, senior *Senior, out chan<- Event) llm.Message {
	type result struct {
		output string
		failed bool
	}
	results := make([]result, len(calls))
	run := func(i int) {
		c := calls[i]
		if box.Has(c.Name) {
			output, failed := box.Call(ctx, c.Name, c.Input)
			senior.Note(callLabel(c.Name, "", c.Input), output, failed)
			if failed && senior.Failed(c.Name) { // the second failure: a stronger model's advice comes with it
				output += "\n" + senior.Ask("The tool "+c.Name+" failed twice in this task. Last error:\n"+clipText(output, 1500))
			}
			results[i] = result{output, failed}
			return
		}
		name, ok := real[c.Name]
		if !ok || gw == nil {
			results[i] = result{"no tool named " + c.Name, true}
			return
		}
		output, failed := callTool(ctx, gw, apiCall{ID: c.ID, Name: name, Args: c.Input})
		if r := []rune(output); len(r) > maxToolOutput {
			output = string(r[:maxToolOutput]) + "\n… (cut: the result is longer)"
		}
		results[i] = result{output, failed}
	}
	called := func(c llm.Block) {
		send(ctx, out, ToolCalled{agentName, callLabel(c.Name, real[c.Name], c.Input)})
	}
	done := func(i int) {
		send(ctx, out, ToolResult{Agent: agentName, Text: resultShown(calls[i].Name, results[i].output, results[i].failed), Failed: results[i].failed})
	}

	together := len(calls) > 1
	for _, c := range calls {
		if !box.Has(c.Name) || !box.ReadOnly(c.Name) || c.Name == "web_fetch" || c.Name == "todo_write" {
			together = false
		}
	}
	if together {
		for _, c := range calls {
			called(c)
		}
		var wg sync.WaitGroup
		for i := range calls {
			wg.Add(1)
			go func() { defer wg.Done(); run(i) }()
		}
		wg.Wait()
		for i := range calls {
			done(i)
		}
	} else {
		for i, c := range calls {
			called(c)
			run(i)
			done(i)
		}
	}

	msg := llm.Message{Role: "user"}
	for i, c := range calls {
		msg.Blocks = append(msg.Blocks, llm.Block{Type: "tool_result", ID: c.ID, Content: results[i].output, IsError: results[i].failed})
	}
	return msg
}

// mainArg is the argument a native tool's call is known by.
var mainArg = map[string]string{"read": "path", "write": "path", "edit": "path", "glob": "pattern", "grep": "pattern",
	"bash": "command", "web_fetch": "url"}

// callLabel is how a call shows on screen: read(main.go), bash(go test
// ./...), run_sql(SELECT …).
func callLabel(name, gatewayName string, input json.RawMessage) string {
	if gatewayName != "" {
		return describeCall(gatewayName, input)
	}
	var args map[string]any
	json.Unmarshal(input, &args)
	if name == "todo_write" {
		n := 0
		if list, ok := args["todos"].([]any); ok {
			n = len(list)
		}
		return fmt.Sprintf("todo_write(%d steps)", n)
	}
	if key, ok := mainArg[name]; ok {
		v := strings.Join(strings.Fields(fmt.Sprint(args[key])), " ")
		if r := []rune(v); len(r) > 80 {
			v = string(r[:80]) + "…"
		}
		return name + "(" + v + ")"
	}
	return describeCall(name, input)
}

// resultLine is what a result shows on screen: the line that says the
// most, and a senior's advice when the gateway added one.
func resultLine(name, output string, failed bool) string {
	text := strings.TrimSpace(output)
	switch {
	case name == "read" && !failed:
		return fmt.Sprintf("%d lines", strings.Count(text, "\n")+1)
	case (name == "grep" || name == "glob") && !failed && text != "no matches" && !strings.HasPrefix(text, "no files"):
		return fmt.Sprintf("%d results", strings.Count(text, "\n")+1)
	case name == "bash" && !failed:
		return lastLine(text) // tests and builds end with the verdict
	case name == "bash" && failed:
		head, rest, _ := strings.Cut(text, "\n")
		if rest != "" {
			return head + " · " + lastLine(rest)
		}
		return head
	}
	return firstLine(text)
}

// resultShown is a result's line on screen, with a senior's advice under
// it when one came.
func resultShown(name, output string, failed bool) string {
	if k := strings.Index(output, "\n↑ "); k >= 0 {
		return resultLine(name, output[:k], failed) + "\n" + strings.TrimSpace(output[k+1:])
	}
	return resultLine(name, output, failed)
}
