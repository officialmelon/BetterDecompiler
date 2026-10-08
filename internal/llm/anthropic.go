package llm

import (
	"context"
	"net/http"
	"strings"
)

// anthropic speaks the Anthropic Messages API.
type anthropic struct{ base }

const anthropicVersion = "2023-06-01"

// Models that accept the server-side refusal fallback ("fallbacks": "default").
var anthropicFallbackModels = map[string]bool{
	"claude-opus-5-5": true, "claude-opus-5": true, "claude-fable-5-1": true, "claude-sonnet-5-5": true,
}

// Model families that accept output_config.effort.
var anthropicEffortPrefixes = []string{
	"claude-opus-4-5", "claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5",
	"claude-sonnet-4-6", "claude-sonnet-5", "claude-haiku-5", "claude-fable-5", "claude-mythos-5",
}

func anthropicSupportsEffort(model string) bool {
	for _, p := range anthropicEffortPrefixes {
		if strings.HasPrefix(model, p) {
			return true
		}
	}
	return false
}

type anResponse struct {
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason  string `json:"stop_reason"`
	StopDetails *struct {
		Category    *string `json:"category"`
		Explanation string  `json:"explanation"`
	} `json:"stop_details"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (c *anthropic) headers(model string, fallbacks bool) map[string]string {
	h := c.authHeaders("x-api-key", "")
	h["anthropic-version"] = anthropicVersion
	if fallbacks {
		h["anthropic-beta"] = "server-side-fallback-2026-07-01"
	}
	return h
}

func (c *anthropic) Complete(ctx context.Context, req Request) (*Response, error) {
	model := c.model(req)
	maxTokens := c.cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 32000
	}
	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"system":     req.System,
		"messages":   []map[string]string{{"role": "user", "content": req.Prompt}},
	}
	effort := c.cfg.Effort
	if effort == "" && anthropicSupportsEffort(model) {
		effort = "medium"
	}
	if effort != "" && effort != "none" {
		body["output_config"] = map[string]string{"effort": effort}
	}
	if c.cfg.Temperature != nil {
		body["temperature"] = *c.cfg.Temperature
	}
	fallbacks := !c.cfg.NoFallbacks && anthropicFallbackModels[model]
	if fallbacks {
		// If a safety classifier declines, the API retries on Anthropic's
		// recommended fallback model instead of returning a refusal.
		body["fallbacks"] = "default"
	}
	var out anResponse
	if err := c.do(ctx, http.MethodPost, c.cfg.BaseURL+"/v1/messages", c.headers(model, fallbacks), body, &out); err != nil {
		return nil, err
	}
	if out.StopReason == "refusal" {
		msg := "the model declined this request"
		if d := out.StopDetails; d != nil {
			if d.Category != nil {
				msg += " (" + *d.Category + ")"
			}
			if d.Explanation != "" {
				msg += ": " + d.Explanation
			}
		}
		return nil, &Error{Provider: c.cfg.Name, Message: msg}
	}
	// When a fallback happened mid-response, only the text after the last
	// fallback block belongs to the final answer.
	start := 0
	for i, blk := range out.Content {
		if blk.Type == "fallback" {
			start = i + 1
		}
	}
	var sb strings.Builder
	for _, blk := range out.Content[start:] {
		if blk.Type == "text" {
			sb.WriteString(blk.Text)
		}
	}
	if out.Model == "" {
		out.Model = model
	}
	return &Response{
		Text:      sb.String(),
		Model:     out.Model,
		Usage:     Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens},
		Truncated: out.StopReason == "max_tokens",
	}, nil
}

func (c *anthropic) ListModels(ctx context.Context) ([]string, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, c.cfg.BaseURL+"/v1/models?limit=1000", c.headers("", false), nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}
