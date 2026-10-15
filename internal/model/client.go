// Package model implements non-streaming, stateless model requests.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	Responses       = "responses"
	ChatCompletions = "chat_completions"
	MaxTimeout      = 180 * time.Second
	MaxOutputTokens = 32768
)

type Config struct {
	BaseURL, APIKey, Mode string
	Timeout               time.Duration
	MaxOutputTokens       int
}

type Request struct {
	Model, Effort, System string
	Messages              []Message
	Tools                 []Tool
	// ToolChoice is empty/auto, none, required, or a declared function name.
	ToolChoice string
}

type Message struct {
	Role        string            `json:"role"`
	Content     string            `json:"content,omitempty"`
	Images      []string          `json:"images,omitempty"`
	Calls       []Call            `json:"calls,omitempty"`
	CallID      string            `json:"call_id,omitempty"`
	Items       []json.RawMessage `json:"items,omitempty"`
	SourceModel string            `json:"source_model,omitempty"`
}

type Tool struct {
	Name, Description string
	Parameters        json.RawMessage
}

type Call struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Response struct {
	Text, Refusal string
	Calls         []Call
	Items         []json.RawMessage
	Usage         Usage
}

// ReasoningTokens is part of OutputTokens, never an additional charge.
type Usage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ReasoningTokens int `json:"reasoning_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

type Error struct {
	Kind       string
	StatusCode int
	Message    string
}

func (e *Error) Error() string { return "model " + e.Kind + ": " + e.Message }

type Client struct {
	config Config
	http   *http.Client
}

func New(config Config, client *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(config.BaseURL, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("MODEL_BASE_URL must be an http(s) base URL without credentials, query or fragment")
	}
	if u.Path == "" {
		u.Path = "/v1"
	}
	config.BaseURL = strings.TrimRight(u.String(), "/")
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("MODEL_API_KEY is required")
	}
	if config.Mode == "" {
		config.Mode = Responses
	}
	if config.Mode != Responses && config.Mode != ChatCompletions {
		return nil, errors.New("MODEL_API_MODE must be responses or chat_completions")
	}
	if config.Timeout == 0 {
		config.Timeout = MaxTimeout
	}
	if config.Timeout <= 0 || config.Timeout > MaxTimeout {
		return nil, errors.New("model timeout must be between zero and 180 seconds")
	}
	if config.MaxOutputTokens == 0 {
		config.MaxOutputTokens = MaxOutputTokens
	}
	if config.MaxOutputTokens < 1 || config.MaxOutputTokens > MaxOutputTokens {
		return nil, errors.New("model output budget must be between 1 and 32768")
	}
	if client == nil {
		client = &http.Client{}
	}
	clone := *client
	// A provider redirect must not forward credentials or silently change endpoints.
	clone.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{config: config, http: &clone}, nil
}

func (c *Client) Generate(ctx context.Context, req Request) (Response, error) {
	if err := validateRequest(&req); err != nil {
		return Response{}, err
	}
	var payload map[string]any
	var err error
	endpoint := "/responses"
	if c.config.Mode == Responses {
		payload, err = responsesRequest(req)
	} else {
		endpoint = "/chat/completions"
		payload, err = chatRequest(req)
	}
	if err != nil {
		return Response{}, err
	}
	if c.config.Mode == Responses {
		payload["max_output_tokens"] = c.config.MaxOutputTokens
	} else {
		payload["max_completion_tokens"] = c.config.MaxOutputTokens
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return Response{}, failure("invalid_request", "cannot encode request")
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.BaseURL+endpoint, bytes.NewReader(data))
	if err != nil {
		return Response{}, failure("invalid_request", "cannot construct request")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	r, err := c.http.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, failure("transport", "request failed")
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return Response{}, &Error{Kind: "http", StatusCode: r.StatusCode, Message: fmt.Sprintf("provider returned HTTP %d", r.StatusCode)}
	}
	const limit = 8 << 20
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, failure("transport", "cannot read response")
	}
	if len(body) > limit {
		return Response{}, failure("invalid_response", "response exceeds size limit")
	}
	var result Response
	if c.config.Mode == Responses {
		result, err = parseResponses(body)
	} else {
		result, err = parseChat(body)
	}
	if err != nil {
		return result, err
	}
	declared := map[string]bool{}
	for _, tool := range req.Tools {
		declared[tool.Name] = true
	}
	seen := map[string]bool{}
	for _, call := range result.Calls {
		if !declared[call.Name] || !validCall(call) || seen[call.ID] || req.ToolChoice == "none" {
			return Response{Usage: result.Usage}, failure("invalid_response", "invalid or undeclared tool call")
		}
		if req.ToolChoice != "" && req.ToolChoice != "auto" && req.ToolChoice != "required" && req.ToolChoice != call.Name {
			return Response{Usage: result.Usage}, failure("invalid_response", "provider returned a different forced tool")
		}
		seen[call.ID] = true
	}
	if req.ToolChoice != "" && req.ToolChoice != "auto" && req.ToolChoice != "none" && len(result.Calls) == 0 {
		return Response{Usage: result.Usage}, failure("invalid_response", "provider omitted required tool")
	}
	return result, nil
}

var functionName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func validateRequest(req *Request) error {
	if strings.TrimSpace(req.Model) == "" {
		return failure("invalid_request", "model is required")
	}
	if req.Effort == "" {
		req.Effort = "medium"
	}
	switch req.Effort {
	case "none", "minimal", "low", "medium", "high", "xhigh":
	default:
		return failure("invalid_request", "unsupported reasoning effort")
	}
	names := map[string]bool{}
	for _, tool := range req.Tools {
		if !functionName.MatchString(tool.Name) || names[tool.Name] {
			return failure("invalid_request", "invalid or duplicate tool name")
		}
		var schema map[string]any
		if json.Unmarshal(tool.Parameters, &schema) != nil || schema["type"] != "object" {
			return failure("invalid_request", "tool parameters must be an object schema")
		}
		names[tool.Name] = true
	}
	switch req.ToolChoice {
	case "", "auto", "none":
	case "required":
		if len(req.Tools) == 0 {
			return failure("invalid_request", "required tool choice needs tools")
		}
	default:
		if !names[req.ToolChoice] {
			return failure("invalid_request", "forced tool is not declared")
		}
	}
	pending := map[string]bool{}
	seen := map[string]bool{}
	for _, original := range req.Messages {
		m, err := visibleMessage(original)
		if err != nil {
			return err
		}
		if m.Role != "user" && m.Role != "assistant" && m.Role != "tool" {
			return failure("invalid_request", "invalid history role")
		}
		if len(m.Calls) > 0 && m.Role != "assistant" {
			return failure("invalid_request", "tool calls require assistant role")
		}
		if len(m.Images) > 0 && m.Role != "user" {
			return failure("invalid_request", "image input requires user role")
		}
		if m.Role != "tool" && len(pending) > 0 {
			return failure("invalid_request", "tool outputs must follow their calls")
		}
		for _, call := range m.Calls {
			if !validCall(call) || seen[call.ID] {
				return failure("invalid_request", "invalid or duplicate historical call")
			}
			pending[call.ID], seen[call.ID] = true, true
		}
		if m.Role == "tool" {
			if !pending[m.CallID] {
				return failure("invalid_request", "orphan or duplicate tool output")
			}
			delete(pending, m.CallID)
		}
	}
	if len(pending) > 0 {
		return failure("invalid_request", "missing tool output")
	}
	return nil
}

func validCall(call Call) bool {
	var args map[string]json.RawMessage
	return call.ID != "" && functionName.MatchString(call.Name) && json.Unmarshal([]byte(call.Arguments), &args) == nil && args != nil
}

func failure(kind, message string) error { return &Error{Kind: kind, Message: message} }
