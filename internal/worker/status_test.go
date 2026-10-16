package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/supertx/qq-tavern/internal/model"
	"github.com/supertx/qq-tavern/internal/state"
)

func TestStatusCommandsDoNotCallModelAndSwitchAllRoleData(t *testing.T) {
	svc, store, sender := workerFixture(t, generateFunc(func(context.Context, model.Request) (model.Response, error) {
		t.Fatal("command called model")
		return model.Response{}, nil
	}))
	n := 0
	run := func(user, text string) string {
		t.Helper()
		n++
		handle(t, svc, workerEvent(fmt.Sprint(n), "g", user, text))
		return sender.requests[len(sender.requests)-1].Text
	}
	run("alice", "/角色 名称 丛雨")
	run("alice", "/角色 描述 神刀的管理者")
	run("alice", "/世界书 添加 常驻 神刀 | 丛雨丸")
	run("alice", "/记忆 设置 喜欢甜食")
	run("alice", "/状态设置 好感度 43")
	run("alice", "/状态项 添加 信任度 数值 成员")
	run("alice", "/状态项 设置 信任度 初始值 10")
	run("alice", "/状态项 设置 信任度 范围 0 100")
	run("alice", "/状态项 设置 信任度 单轮上限 2")
	run("alice", "/状态项 设置 信任度 规则 遵守约定时增加")
	run("alice", "/状态项 添加 当前地点 文本 角色")
	run("alice", "/状态设置 当前地点 神社")
	run("alice", "/状态项 停用 信任度")
	run("alice", "/状态项 启用 信任度")
	if text := run("bob", "/角色状态"); !strings.Contains(text, "好感度：20") || !strings.Contains(text, "当前地点：神社") || !strings.Contains(text, "信任度：10") {
		t.Fatal(text)
	}
	run("bob", "/角色 新建 芳乃")
	if text := run("alice", "/角色状态"); !strings.Contains(text, "芳乃") || strings.Contains(text, "当前地点") {
		t.Fatal(text)
	}
	if text := run("alice", "/好感度 丛雨"); !strings.Contains(text, "43") {
		t.Fatal(text)
	}
	if text := run("alice", "/角色 列表"); !strings.Contains(text, "芳乃（当前）") {
		t.Fatal("query switched role", text)
	}
	run("alice", "/角色 切换 丛雨")
	run("alice", "/角色 名称 小丛雨")
	view, err := store.BindActive(context.Background(), workerEvent("read", "g", "alice", "").Scope)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := view.Snapshot(context.Background(), view.Role().Scope, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Config.Character.Description != "神刀的管理者" || len(snap.Config.Entries) != 1 || snap.Personal != "喜欢甜食" {
		t.Fatal("lost role memories", snap)
	}
	if text := run("alice", "/好感度"); !strings.Contains(text, "43") {
		t.Fatal(text)
	}
	run("alice", "/状态重置 好感度")
	if text := run("alice", "/好感度"); !strings.Contains(text, "20") {
		t.Fatal(text)
	}
	if text := run("bob", "/状态记录"); strings.Contains(text, "43") {
		t.Fatal("leaked another member's audit", text)
	}
	run("alice", "/重置上下文")
	if text := run("alice", "/角色状态"); !strings.Contains(text, "当前地点：神社") {
		t.Fatal(text)
	}
	if text := run("alice", "/状态设置 好感度 NaN"); !strings.Contains(text, "未保存") {
		t.Fatal(text)
	}
}

func TestAutomaticStatusStagedUntilDeliveryAndReplayStripped(t *testing.T) {
	for _, protocol := range []string{model.Responses, model.ChatCompletions} {
		t.Run(protocol, func(t *testing.T) {
			calls := 0
			var view *state.Store
			scope := workerEvent("x", "g", "alice", "").Scope
			svc, store, sender := workerFixture(t, generateFunc(func(ctx context.Context, r model.Request) (model.Response, error) {
				calls++
				switch calls {
				case 1:
					found := false
					for _, tool := range r.Tools {
						if tool.Name == statusTool {
							found = true
						}
					}
					if !found || !strings.Contains(r.System, "当前值：20") {
						t.Fatal("status not provided")
					}
					return model.Response{Calls: []model.Call{{ID: "s1", Name: statusTool, Arguments: `{"changes":[{"name":"好感度","delta":2},{"name":"心情","value":"开心"}],"reason":"遵守约定"}`}}, Items: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"private-status"}`)}}, nil
				case 2:
					snap, err := view.Snapshot(ctx, scope, "alice")
					if err != nil {
						t.Fatal(err)
					}
					d, _ := snap.Status.Definition("好感度")
					if snap.Status.Value(d) != float64(20) {
						t.Fatal("wrote before successful delivery")
					}
					// A new tool ID with equal args must reuse the staged result.
					return model.Response{Calls: []model.Call{{ID: "s2", Name: statusTool, Arguments: `{"changes":[{"name":"好感度","delta":2},{"name":"心情","value":"开心"}],"reason":"遵守约定"}`}, {ID: "s3", Name: statusTool, Arguments: `{"changes":[{"name":"好感度","delta":1}],"reason":"再次加分"}`}}}, nil
				case 3:
					return model.Response{Text: "哼，算汝守信！"}, nil
				default:
					if !strings.Contains(r.System, "当前值：22") {
						t.Fatal("saved status not injected next turn")
					}
					for _, m := range r.Messages {
						for _, c := range m.Calls {
							if c.Name == statusTool {
								t.Fatal("replayed old status call")
							}
						}
						if strings.Contains(m.Content, "遵守约定") || len(m.Items) > 0 {
							t.Fatal("replayed private status data")
						}
					}
					return model.Response{Text: "今日想去哪里？"}, nil
				}
			}))
			svc.cfg.Mode = protocol
			var err error
			view, err = store.BindActive(context.Background(), scope)
			if err != nil {
				t.Fatal(err)
			}
			e := workerEvent("turn", "g", "alice", "我按约定来陪你了")
			handle(t, svc, e)
			handle(t, svc, e)
			if len(sender.requests) != 1 || sender.requests[0].Text != "哼，算汝守信！" || calls != 3 {
				t.Fatal("extra output or duplicate execution", calls, sender.requests)
			}
			snap, err := view.Snapshot(context.Background(), scope, "alice")
			if err != nil {
				t.Fatal(err)
			}
			d, _ := snap.Status.Definition("好感度")
			if snap.Status.Value(d) != float64(22) {
				t.Fatal("incorrect score")
			}
			h, err := view.History(context.Background(), scope, 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(h) != 1 || strings.Contains(string(h[0].Payload), statusTool) || strings.Contains(string(h[0].Payload), "private-status") {
				t.Fatal("internal data persisted in chat")
			}
			handle(t, svc, workerEvent("next", "g", "alice", "今天一起散步"))
		})
	}
}

func TestAutomaticStatusFailuresKeepOldValues(t *testing.T) {
	for _, failure := range []string{"model", "send", "invalid", "empty", "observe"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			svc, store, _ := workerFixture(t, generateFunc(func(context.Context, model.Request) (model.Response, error) {
				calls++
				if calls == 1 {
					delta := 2
					if failure == "invalid" {
						delta = 500
					}
					return model.Response{Calls: []model.Call{{ID: "state", Name: statusTool, Arguments: fmt.Sprintf(`{"changes":[{"name":"好感度","delta":%d}],"reason":"互动"}`, delta)}}}, nil
				}
				if failure == "model" {
					return model.Response{}, errors.New("provider unavailable")
				}
				if failure == "empty" {
					return model.Response{}, nil
				}
				return model.Response{Text: "普通回复"}, nil
			}))
			if failure == "send" {
				sender := &uncertainSender{}
				svc.sender = sender
				svc.tools.sender = sender
			}
			e := workerEvent("event", "g", "alice", "hello")
			e.ObserveOnly = failure == "observe"
			err := svc.Handle(context.Background(), e)
			if (failure == "model" || failure == "send") && err == nil {
				t.Fatal("expected failure")
			}
			if failure == "observe" && calls != 0 {
				t.Fatal("observation called model")
			}
			view, err := store.BindActive(context.Background(), e.Scope)
			if err != nil {
				t.Fatal(err)
			}
			snap, err := view.Snapshot(context.Background(), e.Scope, e.UserID)
			if err != nil {
				t.Fatal(err)
			}
			d, _ := snap.Status.Definition("好感度")
			if snap.Status.Value(d) != float64(20) {
				t.Fatal("failed turn mutated score")
			}
		})
	}
}

func TestBackgroundSummaryPinsRoleAcrossSwitch(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	svc, store, _ := workerFixture(t, generateFunc(func(_ context.Context, r model.Request) (model.Response, error) {
		if !strings.Contains(r.Messages[0].Content, "A的经历") {
			t.Error("wrong summary history")
		}
		close(started)
		<-release
		return model.Response{Text: "A的摘要"}, nil
	}))
	ctx := context.Background()
	e := workerEvent("a", "g", "alice", "A的经历")
	a, err := store.BindActive(ctx, e.Scope)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]model.Message{userMessage(e), {Role: "assistant", Content: "A的回答"}})
	if err = a.AddTurn(ctx, e, "m", raw); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- svc.summarizeRole(ctx, a, e.Scope) }()
	<-started
	if err = store.SelectRole(ctx, workerEvent("switch", "g", "bob", ""), "B", true); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	b, err := store.BindActive(ctx, e.Scope)
	if err != nil {
		t.Fatal(err)
	}
	sa, err := a.Summary(ctx, e.Scope)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := b.Summary(ctx, e.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if sa.Text != "A的摘要" || sb.Text != "" {
		t.Fatal("summary crossed roles")
	}
}

func TestStatusToolRejectsOwnerAndKeepsOtherToolPairs(t *testing.T) {
	svc, store, _ := workerFixture(t, generateFunc(func(context.Context, model.Request) (model.Response, error) { return model.Response{}, nil }))
	e := workerEvent("e", "g", "alice", "")
	view, err := store.BindActive(context.Background(), e.Scope)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := view.Snapshot(context.Background(), e.Scope, e.UserID)
	if err != nil {
		t.Fatal(err)
	}
	session := svc.tools.Session(e)
	session.status = &snap.Status
	result := session.Execute(context.Background(), model.Call{ID: "bad", Name: statusTool, Arguments: `{"member":"bob","changes":[{"name":"好感度","delta":2}],"reason":"指定其他成员"}`})
	if !strings.Contains(result, "error") || session.pending != nil {
		t.Fatal(result)
	}
	clean := withoutStatusTools([]model.Message{{Role: "user", Content: "hello"}, {Role: "assistant", Calls: []model.Call{{ID: "s", Name: statusTool}, {ID: "time", Name: "current_time"}}, Items: []json.RawMessage{json.RawMessage(`{"type":"reasoning"}`)}}, {Role: "tool", CallID: "s", Content: "private"}, {Role: "tool", CallID: "time", Content: "12:00"}, {Role: "assistant", Content: "现在正午"}})
	if len(clean) != 4 || len(clean[1].Calls) != 1 || clean[1].Calls[0].ID != "time" || clean[2].CallID != "time" || len(clean[1].Items) != 0 {
		t.Fatalf("broken pairs: %+v", clean)
	}
}

func TestStateToolWireRoundTripBothProtocols(t *testing.T) {
	for _, protocol := range []string{model.Responses, model.ChatCompletions} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				if !strings.Contains(string(body), statusTool) {
					t.Error("state tool missing on wire")
				}
				w.Header().Set("Content-Type", "application/json")
				if calls.Add(1) == 1 {
					args := `{"changes":[{"name":"好感度","delta":1}],"reason":"诚恳交流"}`
					if protocol == model.Responses {
						_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "function_call", "status": "completed", "call_id": "state", "name": statusTool, "arguments": args}}})
					} else {
						_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "state", "type": "function", "function": map[string]any{"name": statusTool, "arguments": args}}}}}}})
					}
					return
				}
				if !strings.Contains(string(body), "staged") {
					t.Error("tool result not returned to provider")
				}
				if protocol == model.Responses {
					_, _ = io.WriteString(w, `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"嗯，吾记得汝的心意。"}]}]}`)
				} else {
					_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"嗯，吾记得汝的心意。"}}]}`)
				}
			}))
			defer server.Close()
			client, err := model.New(model.Config{BaseURL: server.URL, APIKey: "test-only", Mode: protocol}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			svc, store, sender := workerFixture(t, client)
			svc.cfg.Mode = protocol
			e := workerEvent("wire", "g", "alice", "一起聊聊天")
			handle(t, svc, e)
			view, err := store.BindActive(context.Background(), e.Scope)
			if err != nil {
				t.Fatal(err)
			}
			snap, err := view.Snapshot(context.Background(), e.Scope, e.UserID)
			if err != nil {
				t.Fatal(err)
			}
			d, _ := snap.Status.Definition("好感度")
			if snap.Status.Value(d) != float64(21) || calls.Load() != 2 || len(sender.requests) != 1 {
				t.Fatal("wire roundtrip failed")
			}
		})
	}
}
