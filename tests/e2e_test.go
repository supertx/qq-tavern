package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/supertx/qq-tavern/internal/config"
	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/eventbus"
	"github.com/supertx/qq-tavern/internal/model"
	"github.com/supertx/qq-tavern/internal/qq"
	"github.com/supertx/qq-tavern/internal/state"
	"github.com/supertx/qq-tavern/internal/worker"
)

func startRedis(t *testing.T) string {
	t.Helper()
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		binary = "/opt/homebrew/bin/redis-server"
		if _, err = exec.LookPath(binary); err != nil {
			t.Skip("redis-server is required for the standalone end-to-end test")
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	cmd := exec.Command(binary, "--bind", "127.0.0.1", "--port", fmt.Sprint(port), "--save", "", "--appendonly", "no", "--dir", t.TempDir(), "--logfile", filepath.Join(t.TempDir(), "redis.log"))
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	address := fmt.Sprintf("redis://127.0.0.1:%d/0", port)
	b, _ := eventbus.New(address, "startup")
	for i := 0; i < 100; i++ {
		if b.Ping(context.Background()) == nil {
			return address
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Redis did not start")
	return ""
}

func qqMessage(group, user, id, text string) map[string]any {
	typ := "GROUP_AT_MESSAGE_CREATE"
	author := map[string]string{"member_openid": user, "username": "同名成员"}
	if group == "" {
		typ = "C2C_MESSAGE_CREATE"
		author = map[string]string{"user_openid": user, "username": "同名成员"}
	}
	return map[string]any{"id": "dispatch-" + id, "op": 0, "t": typ, "d": map[string]any{"id": id, "group_openid": group, "author": author, "content": text, "timestamp": time.Now().Format(time.RFC3339Nano)}}
}

type qqReply struct {
	path, content string
	seq           int
}

// This test uses the same independently constructed Gateway/Worker bus instances
// as the binaries, real Redis and SQLite, and simulated QQ/model HTTP protocols.
func TestStandaloneGatewayRedisWorkerQQ(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	appID := "e2e-test-bot"
	botID := domain.Key(appID)
	redisURL := startRedis(t)
	publisher, err := eventbus.New(redisURL, botID)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := eventbus.New(redisURL, botID)
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan map[string]any, 8)
	replies := make(chan qqReply, 16)
	websocketReady := make(chan struct{})
	upgrader := websocket.Upgrader{}
	var qqAPI *httptest.Server
	qqAPI = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			var credentials map[string]string
			if json.NewDecoder(r.Body).Decode(&credentials) != nil || credentials["appId"] != appID || credentials["clientSecret"] != "dummy-qq-secret" {
				t.Error("incorrect QQ credentials")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "dummy-qq-access", "expires_in": 7200})
		case "/gateway/bot":
			_ = json.NewEncoder(w).Encode(map[string]string{"url": "ws" + strings.TrimPrefix(qqAPI.URL, "http") + "/ws"})
		case "/ws":
			conn, e := upgrader.Upgrade(w, r, nil)
			if e != nil {
				return
			}
			defer conn.Close()
			_ = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]int{"heartbeat_interval": 45000}})
			var auth struct {
				Op   int `json:"op"`
				Data struct {
					Token   string `json:"token"`
					Intents int    `json:"intents"`
				} `json:"d"`
			}
			if conn.ReadJSON(&auth) != nil {
				return
			}
			if auth.Op != 2 || auth.Data.Token != "QQBot dummy-qq-access" || auth.Data.Intents != 1<<25 {
				t.Error("incorrect QQ identify")
			}
			_ = conn.WriteJSON(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]string{"session_id": "e2e-session"}})
			close(websocketReady)
			sequence := 2
			for {
				select {
				case <-ctx.Done():
					return
				case event := <-events:
					event["s"] = sequence
					sequence++
					if conn.WriteJSON(event) != nil {
						return
					}
				}
			}
		default:
			if !strings.HasSuffix(r.URL.Path, "/messages") {
				http.NotFound(w, r)
				return
			}
			if r.Header.Get("Authorization") != "QQBot dummy-qq-access" {
				t.Error("missing QQ bearer token")
			}
			var body struct {
				Content   string `json:"content"`
				Sequence  int    `json:"msg_seq"`
				MessageID string `json:"msg_id"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("invalid QQ reply body")
			}
			replies <- qqReply{path: r.URL.Path, content: body.Content, seq: body.Sequence}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "sent-" + body.MessageID})
		}
	}))
	defer qqAPI.Close()
	const internalToken = "dummy-internal-token-more-than-32-characters"
	gateway, err := qq.NewGateway(qq.Config{AppID: appID, Secret: "dummy-qq-secret", APIBase: qqAPI.URL, TokenURL: qqAPI.URL + "/token", InternalToken: internalToken, DBPath: filepath.Join(t.TempDir(), "gateway.sqlite")}, publisher)
	if err != nil {
		t.Fatal(err)
	}
	gatewayHTTP := httptest.NewServer(gateway.Handler())
	defer gatewayHTTP.Close()
	gatewayDone := make(chan struct{})
	go func() { defer close(gatewayDone); _ = gateway.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-gatewayDone; _ = gateway.Close() })
	select {
	case <-websocketReady:
	case <-ctx.Done():
		t.Fatal("QQ websocket failed to connect")
	}
	ready := false
	for i := 0; i < 100; i++ {
		response, e := http.Get(gatewayHTTP.URL + "/health/ready")
		if e == nil {
			ready = response.StatusCode == http.StatusOK
			_ = response.Body.Close()
		}
		if ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("gateway healthcheck did not become ready with QQ and Redis connected")
	}
	var modelCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer dummy-model-key" {
			t.Error("model protocol or auth mismatch")
		}
		var body struct {
			Instructions string            `json:"instructions"`
			Input        []json.RawMessage `json:"input"`
			Reasoning    struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
			Store bool `json:"store"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid model request")
		}
		if body.Reasoning.Effort != "high" || body.Store {
			t.Error("reasoning effort or store flag missing")
		}
		var inputText strings.Builder
		hasClock := false
		hasReasoning := false
		for _, item := range body.Input {
			var value map[string]any
			_ = json.Unmarshal(item, &value)
			if text, ok := value["content"].(string); ok {
				inputText.WriteString(text)
			}
			if value["type"] == "function_call_output" && value["call_id"] == "clock-call" {
				hasClock = true
			}
			if value["type"] == "reasoning" && value["encrypted_content"] == "dummy-encrypted-reasoning" {
				hasReasoning = true
			}
		}
		text := inputText.String()
		if strings.Contains(text, "触发群回复") {
			if !strings.Contains(body.Instructions, "桃花村位于河边") {
				t.Error("explicit group memory not injected")
			}
			if !hasClock {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": "dummy-encrypted-reasoning"}, map[string]any{"type": "function_call", "status": "completed", "call_id": "clock-call", "name": "current_time", "arguments": "{}"}}})
				return
			}
			if !hasReasoning {
				t.Error("reasoning context lost between tool calls")
			}
		} else if strings.Contains(body.Instructions, "桃花村位于河边") {
			t.Error("group memory leaked into another conversation")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]string{"type": "output_text", "text": "模型回复完成"}}}}, "usage": map[string]int{"input_tokens": 12, "output_tokens": 8}})
	}))
	defer provider.Close()
	store, err := state.Open(filepath.Join(t.TempDir(), "worker.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client, err := model.New(model.Config{BaseURL: provider.URL, APIKey: "dummy-model-key", Mode: model.Responses}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := worker.New(config.Config{AppID: appID, Model: "test-model", Mode: model.Responses, Effort: "high", SummaryModel: "test-model"}, store, client, qq.NewRemoteSender(gatewayHTTP.URL, internalToken))
	if err != nil {
		t.Fatal(err)
	}
	runMessage := func(group, user, id, text string) qqReply {
		t.Helper()
		events <- qqMessage(group, user, id, text)
		var delivery *eventbus.Delivery
		for delivery == nil {
			var e error
			delivery, e = consumer.Read(ctx, "e2e-worker")
			if e != nil {
				t.Fatal(e)
			}
		}
		if delivery.Event.Scope.BotID != botID || delivery.Event.UserID != user {
			t.Fatal("gateway/worker bot routing mismatch")
		}
		if err = service.Handle(ctx, delivery.Event); err != nil {
			t.Fatal(err)
		}
		if err = consumer.Ack(ctx, delivery.ID); err != nil {
			t.Fatal(err)
		}
		select {
		case reply := <-replies:
			if reply.seq != 1 {
				t.Errorf("new source message seq=%d", reply.seq)
			}
			return reply
		case <-ctx.Done():
			t.Fatal("missing QQ reply")
		}
		return qqReply{}
	}
	saved := runMessage("group-a", "member-a", "save", "/记住 桃花村位于河边")
	if saved.path != "/v2/groups/group-a/messages" || !strings.Contains(saved.content, "保存") {
		t.Fatalf("memory command reply=%+v", saved)
	}
	if modelCalls.Load() != 0 {
		t.Fatal("explicit memory command unexpectedly used model")
	}
	groupReply := runMessage("group-a", "member-b", "group", "触发群回复")
	if groupReply.path != "/v2/groups/group-a/messages" || groupReply.content != "模型回复完成" || modelCalls.Load() != 2 {
		t.Fatalf("group tool response=%+v calls=%d", groupReply, modelCalls.Load())
	}
	privateReply := runMessage("", "member-a", "private", "私聊提问")
	if privateReply.path != "/v2/users/member-a/messages" || privateReply.content != "模型回复完成" {
		t.Fatalf("private=%+v", privateReply)
	}
	otherGroup := runMessage("group-b", "member-a", "other-group", "另一个群提问")
	if otherGroup.path != "/v2/groups/group-b/messages" {
		t.Fatalf("other group=%+v", otherGroup)
	}
	privateMemory := runMessage("", "member-a", "personal-save", "/记忆 设置 我喜欢红茶")
	if !strings.Contains(privateMemory.content, "保存") {
		t.Fatalf("personal save=%+v", privateMemory)
	}
	groupPersonal := runMessage("group-a", "member-a", "personal-check", "/记忆")
	if strings.Contains(groupPersonal.content, "红茶") {
		t.Fatal("private preference leaked into group personal memory")
	}
	if modelCalls.Load() != 4 {
		t.Fatalf("commands used model or chat retried: calls=%d", modelCalls.Load())
	}
	// A different dispatch ID carrying the same QQ message cannot execute twice.
	duplicate := qqMessage("group-a", "member-b", "group", "触发群回复")
	duplicate["id"] = "different-dispatch-id"
	events <- duplicate
	if d, e := consumer.Read(ctx, "e2e-worker"); e != nil || d != nil {
		t.Fatalf("duplicate dispatch delivered: %v %v", d, e)
	}
	select {
	case reply := <-replies:
		t.Fatalf("unexpected duplicate reply: %+v", reply)
	default:
	}
	history, err := store.History(ctx, domain.Scope{BotID: botID, Kind: "group", ID: "group-a"}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("expected one completed normal group turn, got %d", len(history))
	}
	if !strings.Contains(string(history[0].Payload), "dummy-encrypted-reasoning") || !strings.Contains(string(history[0].Payload), "clock-call") {
		t.Fatal("durable Responses tool context missing")
	}
	if strings.Contains(groupReply.content, "dummy-encrypted-reasoning") {
		t.Fatal("reasoning exposed to QQ")
	}
	cancel()
	<-gatewayDone
}
