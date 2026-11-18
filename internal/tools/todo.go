package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var todoTool = Tool{
	Name: "todo_write",
	Description: "Keep a checklist of the steps of a longer task, so you and the human see where you are. Send the whole list each time; " +
		"status is pending, in_progress (one at a time) or completed. Skip it for a task of one or two steps.",
	Schema: schema(map[string]any{"todos": map[string]any{
		"type": "array",
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"content": str("the step, in a few words"),
			"status":  map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
		}, "required": []string{"content", "status"}},
	}}, "todos"),
	ReadOnly: true,
	run: func(ctx context.Context, b *Box, input json.RawMessage) (string, error) {
		var in struct {
			Todos []Todo `json:"todos"`
		}
		if err := decode(input, &in); err != nil {
			return "", err
		}
		working := 0
		for i, t := range in.Todos {
			if strings.TrimSpace(t.Content) == "" {
				return "", fmt.Errorf("step %d has no content", i+1)
			}
			switch t.Status {
			case "pending", "completed":
			case "in_progress":
				working++
			default:
				return "", fmt.Errorf("step %d: status is pending, in_progress or completed, not %q", i+1, t.Status)
			}
		}
		if working > 1 {
			return "", errors.New("only one step is in_progress at a time")
		}
		b.mu.Lock()
		b.todos = in.Todos
		b.mu.Unlock()
		if b.Todos != nil {
			b.Todos(in.Todos)
		}
		done := 0
		for _, t := range in.Todos {
			if t.Status == "completed" {
				done++
			}
		}
		return fmt.Sprintf("checklist saved: %d of %d done", done, len(in.Todos)), nil
	},
}
