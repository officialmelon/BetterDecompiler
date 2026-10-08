package llm

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// openAI speaks the OpenAI-compatible Chat Completions API.
type openAI struct{ base }

type oaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type oaResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
			Refusal *string `json:"refusal"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

var autoModels sync.Map // base URL -> discovered model

func (c *openAI) Complete(ctx context.Context, req Request) (*Response, error) {
	model := c.model(req)
	if model == "" || model == "auto" {
		m, err := c.autoModel(ctx)
		if err != nil {
			return nil, err
		}
		model = m
	}
	body := map[string]any{
		"model": model,
		"messages": []oaMessage{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.Prompt},
		},
	}
	if c.cfg.MaxTokens > 0 {
		field := c.cfg.MaxTokensField
		if field == "" {
			field = "max_tokens"
		}
		body[field] = c.cfg.MaxTokens
	}
	if c.cfg.Temperature != nil {
		body["temperature"] = *c.cfg.Temperature
	}
	if c.cfg.Effort != "" && c.cfg.Effort != "none" {
		body["reasoning_effort"] = c.cfg.Effort
	}
	if req.JSON && c.cfg.JSONMode {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	var out oaResponse
	if err := c.do(ctx, http.MethodPost, c.cfg.BaseURL+"/chat/completions", c.authHeaders("Authorization", "Bearer "), body, &out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, &Error{Provider: c.cfg.Name, Message: "response contained no choices"}
	}
	ch := out.Choices[0]
	if ch.Message.Refusal != nil && *ch.Message.Refusal != "" {
		return nil, &Error{Provider: c.cfg.Name, Message: "model refused: " + *ch.Message.Refusal}
	}
	if ch.FinishReason == "content_filter" {
		return nil, &Error{Provider: c.cfg.Name, Message: "response blocked by the provider's content filter"}
	}
	text := ""
	if ch.Message.Content != nil {
		text = *ch.Message.Content
	}
	if out.Model == "" {
		out.Model = model
	}
	return &Response{
		Text:      text,
		Model:     out.Model,
		Usage:     Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens},
		Truncated: ch.FinishReason == "length",
	}, nil
}

func (c *openAI) autoModel(ctx context.Context) (string, error) {
	if m, ok := autoModels.Load(c.cfg.BaseURL); ok {
		return m.(string), nil
	}
	models, err := c.ListModels(ctx)
	if err != nil {
		return "", fmt.Errorf("%s: no model configured and model discovery failed: %w", c.cfg.Name, err)
	}
	if len(models) == 0 {
		return "", &Error{Provider: c.cfg.Name, Message: "no model configured and the server reports no models (load one first)"}
	}
	autoModels.Store(c.cfg.BaseURL, models[0])
	return models[0], nil
}

func (c *openAI) ListModels(ctx context.Context) ([]string, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, c.cfg.BaseURL+"/models", c.authHeaders("Authorization", "Bearer "), nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" && !strings.Contains(m.ID, "embed") {
			ids = append(ids, m.ID)
		}
	}
	// Local servers list the loaded model first; keep their order.
	if !strings.Contains(c.cfg.BaseURL, "localhost") && !strings.Contains(c.cfg.BaseURL, "127.0.0.1") {
		sort.Strings(ids)
	}
	return ids, nil
}
