package qq

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/supertx/qq-tavern/internal/domain"
)

const internalTestToken = "test-internal-token-at-least-24"

func testRequest() domain.SendRequest {
	now := time.Now()
	return domain.SendRequest{Event: domain.Event{ID: "dispatch", MessageID: "message", Scope: domain.Scope{BotID: domain.Key("app"), Kind: "group", ID: "group"}, UserID: "member", Text: "hello", CreatedAt: now, Deadline: now.Add(285 * time.Second)}, Key: "final", Text: "reply"}
}
func newTestGateway(t *testing.T, endpoint, path string, p Publisher) *Gateway {
	t.Helper()
	if path == "" {
		path = filepath.Join(t.TempDir(), "gateway.sqlite")
	}
	g, err := NewGateway(Config{AppID: "app", Secret: "test-secret", APIBase: endpoint, TokenURL: endpoint + "/token", InternalToken: internalTestToken, DBPath: path}, p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}
func tokenResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"access_token":"test-access","expires_in":"7200"}`))
}

func TestSendAuthChunkingAndDurableDedup(t *testing.T) {
	var mu sync.Mutex
	var messages []map[string]any
	tokens := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			mu.Lock()
			tokens++
			mu.Unlock()
			tokenResponse(w)
			return
		}
		if r.Header.Get("Authorization") != "QQBot test-access" {
			t.Error("missing QQ token")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		messages = append(messages, body)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"sent"}`))
	}))
	defer api.Close()
	dbPath := filepath.Join(t.TempDir(), "gateway.sqlite")
	g := newTestGateway(t, api.URL, dbPath, nil)
	handler := httptest.NewServer(g.Handler())
	defer handler.Close()
	req := testRequest()
	req.Text = strings.Repeat("你好", 1801)
	sender := NewRemoteSender(handler.URL, internalTestToken)
	if err := NewRemoteSender(handler.URL, "wrong").Send(context.Background(), req); err == nil {
		t.Fatal("unauthenticated send accepted")
	}
	if err := sender.Send(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := sender.Send(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(messages) != 3 || tokens != 1 {
		t.Fatalf("messages=%d tokens=%d", len(messages), tokens)
	}
	for i, body := range messages {
		if body["msg_seq"] != float64(i+1) || body["msg_id"] != "message" || len([]rune(body["content"].(string))) > 1800 {
			t.Fatalf("body=%v", body)
		}
	}
	mu.Unlock()
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := newTestGateway(t, api.URL, dbPath, nil)
	if err := restarted.Send(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(messages) != 3 {
		t.Fatal("receipt did not survive restart")
	}
	mu.Unlock()
	req.Text = "changed"
	if err := restarted.Send(context.Background(), req); err == nil {
		t.Fatal("idempotency key reused with changed content")
	}
	req = testRequest()
	req.Key = "second"
	req.Text = strings.Repeat("x", 5400)
	if err := restarted.Send(context.Background(), req); err == nil {
		t.Fatal("per-source message reply cap exceeded")
	}
}
func TestAmbiguousSendNeverReplayed(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenResponse(w)
			return
		}
		mu.Lock()
		calls++
		mu.Unlock()
		http.Error(w, "failed", http.StatusBadGateway)
	}))
	defer api.Close()
	path := filepath.Join(t.TempDir(), "gateway.sqlite")
	g := newTestGateway(t, api.URL, path, nil)
	req := testRequest()
	if err := g.Send(context.Background(), req); err == nil {
		t.Fatal("failure not reported")
	}
	_ = g.Close()
	g = newTestGateway(t, api.URL, path, nil)
	if err := g.Send(context.Background(), req); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("got %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("replayed %d calls", calls)
	}
}
func TestMediaExpiryScopeAndTruncation(t *testing.T) {
	var bodies []map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenResponse(w)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if strings.HasSuffix(r.URL.Path, "/files") {
			_, _ = w.Write([]byte(`{"file_info":"opaque-file-info"}`))
		} else {
			_, _ = w.Write([]byte(`{"id":"sent"}`))
		}
	}))
	defer api.Close()
	g := newTestGateway(t, api.URL, "", nil)
	req := testRequest()
	req.Text = ""
	req.Media = &domain.Media{Kind: "image", MIME: "image/png", Data: []byte("image-bytes")}
	if err := g.Send(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0]["file_data"] != base64.StdEncoding.EncodeToString([]byte("image-bytes")) || bodies[0]["srv_send_msg"] != false || bodies[1]["msg_type"] != float64(7) {
		t.Fatalf("bodies=%v", bodies)
	}
	req = testRequest()
	req.Event.Scope.BotID = "other"
	if g.Send(context.Background(), req) == nil {
		t.Fatal("cross-bot send allowed")
	}
	req = testRequest()
	req.Event.Deadline = time.Now().Add(-time.Second)
	if !errors.Is(g.Send(context.Background(), req), ErrExpired) {
		t.Fatal("expired send allowed")
	}
	req = testRequest()
	req.Event.ObserveOnly = true
	if g.Send(context.Background(), req) == nil {
		t.Fatal("observation triggered reply")
	}
	chunks := SplitText(strings.Repeat("雨", 10000), 5)
	if len(chunks) != 5 || len([]rune(chunks[4])) > 1800 || !strings.Contains(chunks[4], "省略") {
		t.Fatalf("chunks=%v", chunks)
	}
}

type recordingPublisher struct {
	events []domain.Event
	fail   bool
}

func (p *recordingPublisher) Publish(_ context.Context, event domain.Event) error {
	if p.fail {
		return errors.New("Redis down")
	}
	p.events = append(p.events, event)
	return nil
}

func TestWebsocketResumeAfterPublishAndNoCursorAdvanceOnFailure(t *testing.T) {
	for _, publishFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(publishFailure), func(t *testing.T) {
			p := &recordingPublisher{fail: publishFailure}
			var api *httptest.Server
			var identifies chan payload = make(chan payload, 2)
			upgrader := websocket.Upgrader{}
			api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token":
					tokenResponse(w)
				case "/gateway/bot":
					_ = json.NewEncoder(w).Encode(map[string]string{"url": "ws" + strings.TrimPrefix(api.URL, "http") + "/ws"})
				case "/ws":
					conn, err := upgrader.Upgrade(w, r, nil)
					if err != nil {
						return
					}
					defer conn.Close()
					_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 45000}})
					var auth payload
					if conn.ReadJSON(&auth) != nil {
						return
					}
					identifies <- auth
					if auth.Op == 2 {
						_ = conn.WriteJSON(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]string{"session_id": "session"}})
						var event map[string]any
						_ = json.Unmarshal(eventJSON(t, "GROUP_AT_MESSAGE_CREATE", "u", time.Now()), &event)
						event["s"] = 2
						_ = conn.WriteJSON(event)
					}
					_ = conn.WriteJSON(map[string]any{"op": 7})
				}
			}))
			defer api.Close()
			g := newTestGateway(t, api.URL, "", p)
			session := ""
			var sequence int64
			if err := g.connect(context.Background(), &session, &sequence); err == nil {
				t.Fatal("reconnect not returned")
			}
			first := <-identifies
			if first.Op != 2 {
				t.Fatal("initial connection did not identify")
			}
			expected := int64(2)
			if publishFailure {
				expected = 1
			}
			if session != "session" || sequence != expected {
				t.Fatalf("session=%s seq=%d", session, sequence)
			}
			// A new Gateway must resume from SQLite, not the old local cursor.
			path := g.cfg.DBPath
			if err := g.Close(); err != nil {
				t.Fatal(err)
			}
			g = newTestGateway(t, api.URL, path, p)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- g.Run(ctx) }()
			var second payload
			select {
			case second = <-identifies:
			case <-time.After(3 * time.Second):
				cancel()
				t.Fatal("restart did not resume")
			}
			cancel()
			<-done
			var resume struct {
				Session  string `json:"session_id"`
				Sequence int64  `json:"seq"`
			}
			_ = json.Unmarshal(second.Data, &resume)
			if second.Op != 6 || resume.Session != "session" || resume.Sequence != expected {
				t.Fatalf("resume=%+v", second)
			}
		})
	}
}
