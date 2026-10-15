package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/supertx/qq-tavern/internal/config"
	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/model"
	"github.com/supertx/qq-tavern/internal/state"
)

type toolsSender struct {
	requests []domain.SendRequest
	deadline time.Time
}

func (s *toolsSender) Send(ctx context.Context, r domain.SendRequest) error {
	s.requests = append(s.requests, r)
	s.deadline, _ = ctx.Deadline()
	return ctx.Err()
}

type toolsTransport func(*http.Request) (*http.Response, error)

func (f toolsTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func toolsEvent(group, id string) domain.Event {
	now := time.Now()
	return domain.Event{ID: id, MessageID: id, Scope: domain.Scope{BotID: domain.Key("app"), Kind: "group", ID: group}, UserID: "member", UserName: "同名", Text: "question", CreatedAt: now, Deadline: now.Add(285 * time.Second)}
}
func toolResultError(t *testing.T, result string) string {
	t.Helper()
	var v struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(result), &v); err != nil {
		t.Fatal(err)
	}
	return v.Error
}

func TestToolsDisabledAndStrictParameters(t *testing.T) {
	tools := NewTools(config.Config{}, nil, &toolsSender{})
	session := tools.Session(toolsEvent("g", "e"))
	for _, def := range tools.Definitions() {
		switch def.Name {
		case "generate_image", "edit_image", "speak", "web_search":
			t.Fatalf("disabled tool exposed: %s", def.Name)
		}
	}
	for _, call := range []model.Call{{Name: "generate_image", Arguments: `{"prompt":"test"}`}, {Name: "current_time", Arguments: `{"group_id":"other"}`}, {Name: "current_time", Arguments: `null`}, {Name: "current_time", Arguments: `{} {}`}} {
		result := session.Execute(context.Background(), call)
		if toolResultError(t, result) == "" {
			t.Fatalf("invalid or disabled request accepted: %+v => %s", call, result)
		}
	}
	if got := session.Execute(context.Background(), model.Call{Name: "current_time", Arguments: `{}`}); !strings.Contains(got, `"time"`) {
		t.Fatal(got)
	}
}

func TestWebReadRejectsPrivateAddressesAndCachesFailedURL(t *testing.T) {
	tools := NewTools(config.Config{}, nil, &toolsSender{})
	calls := 0
	tools.public = &http.Client{Transport: toolsTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("blocked")), Request: r}, nil
	})}
	session := tools.Session(toolsEvent("g", "e"))
	for _, raw := range []string{"http://127.0.0.1/", "http://169.254.169.254/latest/meta-data/", "http://localhost/", "http://[::1]/"} {
		result := session.Execute(context.Background(), model.Call{Name: "web_open", Arguments: stringify(map[string]string{"url": raw})})
		if toolResultError(t, result) == "" {
			t.Fatalf("private URL accepted: %s", raw)
		}
	}
	if calls != 0 {
		t.Fatal("private URL reached transport")
	}
	first := session.Execute(context.Background(), model.Call{ID: "one", Name: "web_open", Arguments: `{"url":"https://example.com/blocked"}`})
	second := session.Execute(context.Background(), model.Call{ID: "two", Name: "web_open", Arguments: `{ "url" : "https://example.com/blocked" }`})
	if first != second || !strings.Contains(first, "403") || calls != 1 {
		t.Fatalf("failed URL retried: calls=%d first=%s second=%s", calls, first, second)
	}
}

func TestSearchLimitAndScopeBoundHistory(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("format") != "json" {
			t.Error("missing JSON format")
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"result","url":"https://example.com/","content":"short result"}]}`))
	}))
	defer server.Close()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, g := range []string{"here", "other"} {
		raw, _ := json.Marshal([]model.Message{{Role: "user", Content: "sharedkeyword " + g}})
		if err = store.AddTurn(context.Background(), toolsEvent(g, g), "test-model", raw); err != nil {
			t.Fatal(err)
		}
	}
	tools := NewTools(config.Config{SearchURL: server.URL}, store, &toolsSender{})
	session := tools.Session(toolsEvent("here", "request"))
	for i := 0; i < 4; i++ {
		got := session.Execute(context.Background(), model.Call{Name: "web_search", Arguments: fmt.Sprintf(`{"query":"query%d"}`, i)})
		if i == 3 && toolResultError(t, got) == "" {
			t.Fatal("fourth search accepted")
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("searches=%d", calls.Load())
	}
	got := session.Execute(context.Background(), model.Call{Name: "history_search", Arguments: `{"query":"sharedkeyword"}`})
	if !strings.Contains(got, "sharedkeyword here") || strings.Contains(got, "sharedkeyword other") {
		t.Fatalf("scope leaked: %s", got)
	}
}

func TestImageUsesRemainingBudgetAndSendsOnce(t *testing.T) {
	sender := &toolsSender{}
	tools := NewTools(config.Config{ImageBase: "https://images.example", ImageKey: "dummy-image-key", ImageModel: "test-image"}, nil, sender)
	providerCalls := 0
	tools.client = &http.Client{Transport: toolsTransport(func(r *http.Request) (*http.Response, error) {
		providerCalls++
		d, ok := r.Context().Deadline()
		if !ok || time.Until(d) < 80*time.Second {
			t.Error("image request did not retain shared turn budget")
		}
		if r.URL.Path != "/v1/images/generations" || r.Header.Get("Authorization") != "Bearer dummy-image-key" {
			t.Error("image endpoint or credentials incorrect")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aW1hZ2U="}]}`)), Request: r}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session := tools.Session(toolsEvent("g", "request"))
	first := session.Execute(ctx, model.Call{ID: "one", Name: "generate_image", Arguments: `{"prompt":"a flower"}`})
	second := session.Execute(ctx, model.Call{ID: "two", Name: "generate_image", Arguments: `{ "prompt" : "a flower" }`})
	if !strings.Contains(first, `"sent":true`) || first != second || providerCalls != 1 || len(sender.requests) != 1 || !session.Sent {
		t.Fatalf("first=%s second=%s calls=%d sends=%d", first, second, providerCalls, len(sender.requests))
	}
	if sender.requests[0].Event.Scope.ID != "g" || string(sender.requests[0].Media.Data) != "image" {
		t.Fatal("incorrect media target or body")
	}
}

func TestOversizedGeneratedMediaRejectedBeforeSend(t *testing.T) {
	sender := &toolsSender{}
	tools := NewTools(config.Config{ImageBase: "https://images.example", ImageKey: "dummy", ImageModel: "test-image", SpeechBase: "https://speech.example", SpeechKey: "dummy", SpeechModel: "test-speech"}, nil, sender)
	oversized := strings.Repeat("x", (5<<20)+1)
	tools.client = &http.Client{Transport: toolsTransport(func(r *http.Request) (*http.Response, error) {
		data := oversized
		if strings.Contains(r.URL.Path, "images") {
			data = `{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString([]byte(oversized)) + `"}]}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(data)), Request: r}, nil
	})}
	for _, call := range []model.Call{{ID: "image", Name: "generate_image", Arguments: `{"prompt":"test"}`}, {ID: "speech", Name: "speak", Arguments: `{"text":"test"}`}} {
		session := tools.Session(toolsEvent("g", call.ID))
		result := session.Execute(context.Background(), call)
		if toolResultError(t, result) == "" {
			t.Fatalf("oversized %s accepted: %s", call.Name, result)
		}
	}
	if len(sender.requests) != 0 {
		t.Fatal("oversized output reached sender")
	}
}
