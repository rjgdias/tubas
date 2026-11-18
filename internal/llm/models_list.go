package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

// Lister lists the models a key can use: the way /connect checks a key
// before saving it.
type Lister interface {
	Models(ctx context.Context) ([]string, error)
}

// Models lists the models of an Anthropic-style endpoint.
func (c *Anthropic) Models(ctx context.Context) ([]string, error) {
	client := c.client()
	pager := client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
	var ids []string
	for pager.Next() {
		ids = append(ids, pager.Current().ID)
		if len(ids) >= 500 {
			break
		}
	}
	return ids, pager.Err()
}

// Models lists the models of an OpenAI-compatible endpoint.
func (c *OpenAI) Models(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Base, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return nil, &HTTPError{Status: resp.StatusCode, Body: firstLine(string(body))}
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct { // Ollama's own shape
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("the model list isn't JSON: %s", firstLine(string(body)))
	}
	var ids []string
	for _, m := range list.Data {
		ids = append(ids, m.ID)
	}
	for _, m := range list.Models {
		ids = append(ids, m.Name)
	}
	return ids, nil
}
