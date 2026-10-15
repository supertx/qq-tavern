package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var testTool = Tool{Name: "search", Description: "Search documents", Parameters: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`)}

const textOutput = `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":12,"output_tokens":19,"output_tokens_details":{"reasoning_tokens":7}}}`

func newTestClient(t *testing.T, mode string, handler http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c, err := New(Config{BaseURL: s.URL + "/v1", APIKey: "unit-test-placeholder", Mode: mode}, s.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func requestBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Error(err)
	}
	if r.Header.Get("Authorization") != "Bearer unit-test-placeholder" || r.Header.Get("Content-Type") != "application/json" {
		t.Error("missing request headers")
	}
	return body
}

func TestResponsesEffortAndImages(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh"} {
		t.Run(effort, func(t *testing.T) {
			c := newTestClient(t, Responses, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses" {
					t.Errorf("path %s", r.URL.Path)
				}
				body := requestBody(t, r)
				if body["reasoning"].(map[string]any)["effort"] != effort || body["store"] != false || body["stream"] != false || body["max_output_tokens"] != float64(MaxOutputTokens) {
					t.Error(body)
				}
				if body["instructions"] != "system" || body["include"].([]any)[0] != "reasoning.encrypted_content" {
					t.Error(body)
				}
				parts := body["input"].([]any)[0].(map[string]any)["content"].([]any)
				if parts[1].(map[string]any)["type"] != "input_image" || parts[1].(map[string]any)["image_url"] != "https://example.invalid/image.png" {
					t.Error(parts)
				}
				_, _ = w.Write([]byte(textOutput))
			})
			r, err := c.Generate(context.Background(), Request{Model: "test-model", Effort: effort, System: "system", Messages: []Message{{Role: "user", Content: "look", Images: []string{"https://example.invalid/image.png"}}}})
			if err != nil || r.Text != "answer" || r.Usage.TotalTokens != 31 || r.Usage.ReasoningTokens != 7 || r.Usage.OutputTokens != 19 {
				t.Fatalf("%+v %v", r, err)
			}
		})
	}
}

func TestResponsesToolRoundTrip(t *testing.T) {
	const first = `{"status":"completed","output":[{"id":"rs_a","type":"reasoning","encrypted_content":"opaque-reasoning","summary":[]},{"id":"fc_a","type":"function_call","status":"completed","call_id":"call_a","name":"search","arguments":"{\"q\":\"news\"}"}]}`
	var calls atomic.Int32
	c := newTestClient(t, Responses, func(w http.ResponseWriter, r *http.Request) {
		body := requestBody(t, r)
		tool := body["tools"].([]any)[0].(map[string]any)
		if tool["strict"] != false || tool["name"] != "search" {
			t.Error(tool)
		}
		if calls.Add(1) == 1 {
			if body["tool_choice"].(map[string]any)["name"] != "search" {
				t.Error(body)
			}
			_, _ = w.Write([]byte(first))
			return
		}
		items := body["input"].([]any)
		if len(items) != 4 {
			t.Errorf("items %+v", items)
		} else {
			if items[1].(map[string]any)["encrypted_content"] != "opaque-reasoning" {
				t.Error("lost reasoning")
			}
			if items[2].(map[string]any)["call_id"] != "call_a" || items[3].(map[string]any)["call_id"] != "call_a" || items[3].(map[string]any)["type"] != "function_call_output" {
				t.Error("lost pairing")
			}
		}
		_, _ = w.Write([]byte(textOutput))
	})
	input := Message{Role: "user", Content: "search"}
	r, err := c.Generate(context.Background(), Request{Model: "test-model", Messages: []Message{input}, Tools: []Tool{testTool}, ToolChoice: "search"})
	if err != nil || len(r.Calls) != 1 || r.Text != "" || len(r.Items) != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = c.Generate(context.Background(), Request{Model: "test-model", Messages: []Message{input, {Role: "assistant", Items: r.Items, SourceModel: "test-model"}, {Role: "tool", CallID: r.Calls[0].ID, Content: "result"}}, Tools: []Tool{testTool}})
	if err != nil || r.Text != "answer" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestSwitchModelAndProtocolPreservesVisibleHistory(t *testing.T) {
	items := []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"private-opaque","summary":[{"type":"summary_text","text":"internal"}]}`), json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"visible"}]}`), json.RawMessage(`{"type":"function_call","call_id":"c1","name":"search","arguments":"{}"}`)}
	for _, mode := range []string{Responses, ChatCompletions} {
		t.Run(mode, func(t *testing.T) {
			c := newTestClient(t, mode, func(w http.ResponseWriter, r *http.Request) {
				body := requestBody(t, r)
				encoded, _ := json.Marshal(body)
				if strings.Contains(string(encoded), "private-opaque") || strings.Contains(string(encoded), "internal") || !strings.Contains(string(encoded), "visible") || !strings.Contains(string(encoded), "c1") {
					t.Error(string(encoded))
				}
				if mode == Responses {
					_, _ = w.Write([]byte(textOutput))
				} else {
					_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"answer"}}]}`))
				}
			})
			r, err := c.Generate(context.Background(), Request{Model: "other-model", Messages: []Message{{Role: "assistant", Items: items, SourceModel: "old-model"}, {Role: "tool", CallID: "c1", Content: "result"}}, Tools: []Tool{testTool}})
			if err != nil || r.Text != "answer" {
				t.Fatalf("%+v %v", r, err)
			}
		})
	}
}

func TestChatRequestAndToolCalls(t *testing.T) {
	c := newTestClient(t, ChatCompletions, func(w http.ResponseWriter, r *http.Request) {
		body := requestBody(t, r)
		if r.URL.Path != "/v1/chat/completions" || body["reasoning_effort"] != "medium" || body["max_completion_tokens"] != float64(MaxOutputTokens) {
			t.Error(body)
		}
		tool := body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
		if tool["strict"] != false || body["tool_choice"].(map[string]any)["function"].(map[string]any)["name"] != "search" {
			t.Error(body)
		}
		messages := body["messages"].([]any)
		if messages[0].(map[string]any)["role"] != "system" || messages[1].(map[string]any)["content"].([]any)[1].(map[string]any)["type"] != "image_url" {
			t.Error(messages)
		}
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"search","arguments":"{}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":12}}}`))
	})
	r, err := c.Generate(context.Background(), Request{Model: "test", System: "system", Messages: []Message{{Role: "user", Content: "look", Images: []string{"https://example.invalid/image.png"}}}, Tools: []Tool{testTool}, ToolChoice: "search"})
	if err != nil || len(r.Calls) != 1 || r.Usage.TotalTokens != 30 || r.Usage.ReasoningTokens != 12 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestNoRunnableCallsOnUnsafeResults(t *testing.T) {
	for _, tc := range []struct{ name, mode, body, kind string }{
		{"incomplete", Responses, `{"status":"incomplete","output":[{"type":"function_call","call_id":"c","name":"search","arguments":"{}"}]}`, "incomplete"},
		{"partial_item", Responses, `{"status":"completed","output":[{"type":"function_call","status":"in_progress","call_id":"c","name":"search","arguments":"{}"}]}`, "incomplete"},
		{"bad_second_call", Responses, `{"status":"completed","output":[{"type":"function_call","call_id":"c","name":"search","arguments":"{}"},{"type":"function_call","call_id":"d","name":"search","arguments":"{broken"}]}`, "invalid_response"},
		{"undeclared", Responses, `{"status":"completed","output":[{"type":"function_call","call_id":"c","name":"execute","arguments":"{}"}]}`, "invalid_response"},
		{"duplicate", Responses, `{"status":"completed","output":[{"type":"function_call","call_id":"c","name":"search","arguments":"{}"},{"type":"function_call","call_id":"c","name":"search","arguments":"{}"}]}`, "invalid_response"},
		{"refusal", Responses, `{"status":"completed","output":[{"type":"function_call","call_id":"c","name":"search","arguments":"{}"},{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"declined"}]}]}`, "refusal"},
		{"reasoning_only", Responses, `{"status":"completed","output":[{"type":"reasoning","encrypted_content":"opaque"}]}`, "empty"},
		{"bad_json", Responses, `{`, "invalid_response"},
		{"chat_length", ChatCompletions, `{"choices":[{"finish_reason":"length","message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"search","arguments":"{}"}}]}}]}`, "incomplete"},
		{"chat_refusal", ChatCompletions, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","refusal":"declined","tool_calls":[{"id":"c","type":"function","function":{"name":"search","arguments":"{}"}}]}}]}`, "refusal"},
		{"chat_bad_args", ChatCompletions, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"search","arguments":"[]"}}]}}]}`, "invalid_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, tc.mode, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) })
			r, err := c.Generate(context.Background(), Request{Model: "test", Tools: []Tool{testTool}})
			var typed *Error
			if !errors.As(err, &typed) || typed.Kind != tc.kind || len(r.Calls) != 0 || len(r.Items) != 0 {
				t.Fatalf("%+v %v", r, err)
			}
			if tc.kind == "refusal" && r.Refusal != "declined" {
				t.Error("lost refusal")
			}
		})
	}
}

func TestHTTPFailureDoesNotFallbackOrExposeProviderBody(t *testing.T) {
	var n atomic.Int32
	c := newTestClient(t, Responses, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(503)
		_, _ = w.Write([]byte("secret-provider-body"))
	})
	_, err := c.Generate(context.Background(), Request{Model: "test"})
	var typed *Error
	if !errors.As(err, &typed) || typed.StatusCode != 503 || strings.Contains(err.Error(), "secret") || n.Load() != 1 {
		t.Fatal(err, n.Load())
	}
}

func TestCallerDeadlineBoundsModelTimeout(t *testing.T) {
	c := newTestClient(t, Responses, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Generate(ctx, Request{Model: "test"})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatal(err)
	}
}

func TestRejectInvalidHistoryBeforeRequest(t *testing.T) {
	c := newTestClient(t, Responses, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid request reached provider") })
	for _, messages := range [][]Message{
		{{Role: "tool", CallID: "orphan", Content: "result"}},
		{{Role: "assistant", Calls: []Call{{ID: "c", Name: "search", Arguments: "{}"}}}},
		{{Role: "assistant", Calls: []Call{{ID: "c", Name: "search", Arguments: "{}"}}}, {Role: "user", Content: "interruption"}},
		{{Role: "system", Content: "invalid history override"}},
	} {
		_, err := c.Generate(context.Background(), Request{Model: "test", Messages: messages})
		var typed *Error
		if !errors.As(err, &typed) || typed.Kind != "invalid_request" {
			t.Fatal(err)
		}
	}
}

func TestConfiguration(t *testing.T) {
	for _, config := range []Config{
		{BaseURL: "https://example.invalid", APIKey: "test", Mode: "unknown"},
		{BaseURL: "https://user:password@example.invalid", APIKey: "test"},
		{BaseURL: "https://example.invalid?secret=x", APIKey: "test"},
		{BaseURL: "https://example.invalid", APIKey: "test", Timeout: 181 * time.Second},
		{BaseURL: "https://example.invalid", APIKey: "test", MaxOutputTokens: 32769},
		{BaseURL: "https://example.invalid"},
	} {
		if _, err := New(config, nil); err == nil {
			t.Errorf("accepted %+v", config)
		}
	}
	c, err := New(Config{BaseURL: "https://example.invalid/", APIKey: "test"}, nil)
	if err != nil || c.config.BaseURL != "https://example.invalid/v1" || c.config.Timeout != MaxTimeout {
		t.Fatal(c, err)
	}
}
