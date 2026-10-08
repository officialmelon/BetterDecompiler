package llm

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// gemini speaks the Google Gemini generateContent API.
type gemini struct{ base }

type gmPart struct {
	Text    string `json:"text,omitempty"`
	Thought bool   `json:"thought,omitempty"`
}

type gmContent struct {
	Role  string   `json:"role,omitempty"`
	Parts []gmPart `json:"parts"`
}

type gmResponse struct {
	Candidates []struct {
		Content      gmContent `json:"content"`
		FinishReason string    `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
	ModelVersion string `json:"modelVersion"`
}

func (c *gemini) Complete(ctx context.Context, req Request) (*Response, error) {
	model := strings.TrimPrefix(c.model(req), "models/")
	gen := map[string]any{}
	if c.cfg.MaxTokens > 0 {
		gen["maxOutputTokens"] = c.cfg.MaxTokens
	}
	if c.cfg.Temperature != nil {
		gen["temperature"] = *c.cfg.Temperature
	}
	if req.JSON && c.cfg.JSONMode {
		gen["responseMimeType"] = "application/json"
	}
	if c.cfg.Effort != "" && c.cfg.Effort != "none" {
		gen["thinkingConfig"] = map[string]string{"thinkingLevel": c.cfg.Effort}
	}
	body := map[string]any{
		"systemInstruction": gmContent{Parts: []gmPart{{Text: req.System}}},
		"contents":          []gmContent{{Role: "user", Parts: []gmPart{{Text: req.Prompt}}}},
	}
	if len(gen) > 0 {
		body["generationConfig"] = gen
	}
	endpoint := c.cfg.BaseURL + "/models/" + url.PathEscape(model) + ":generateContent"
	var out gmResponse
	if err := c.do(ctx, http.MethodPost, endpoint, c.authHeaders("x-goog-api-key", ""), body, &out); err != nil {
		return nil, err
	}
	if out.PromptFeedback != nil && out.PromptFeedback.BlockReason != "" {
		return nil, &Error{Provider: c.cfg.Name, Message: "prompt blocked: " + out.PromptFeedback.BlockReason}
	}
	if len(out.Candidates) == 0 {
		return nil, &Error{Provider: c.cfg.Name, Message: "response contained no candidates"}
	}
	cand := out.Candidates[0]
	switch cand.FinishReason {
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "BLOCKLIST", "SPII":
		return nil, &Error{Provider: c.cfg.Name, Message: "response blocked: " + cand.FinishReason}
	}
	var sb strings.Builder
	for _, p := range cand.Content.Parts {
		if !p.Thought {
			sb.WriteString(p.Text)
		}
	}
	served := out.ModelVersion
	if served == "" {
		served = model
	}
	return &Response{
		Text:      sb.String(),
		Model:     served,
		Usage:     Usage{InputTokens: out.UsageMetadata.PromptTokenCount, OutputTokens: out.UsageMetadata.CandidatesTokenCount},
		Truncated: cand.FinishReason == "MAX_TOKENS",
	}, nil
}

func (c *gemini) ListModels(ctx context.Context) ([]string, error) {
	var out struct {
		Models []struct {
			Name    string   `json:"name"`
			Methods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := c.do(ctx, http.MethodGet, c.cfg.BaseURL+"/models?pageSize=1000", c.authHeaders("x-goog-api-key", ""), nil, &out); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range out.Models {
		ok := len(m.Methods) == 0
		for _, meth := range m.Methods {
			if meth == "generateContent" {
				ok = true
			}
		}
		if ok {
			ids = append(ids, strings.TrimPrefix(m.Name, "models/"))
		}
	}
	sort.Strings(ids)
	return ids, nil
}
