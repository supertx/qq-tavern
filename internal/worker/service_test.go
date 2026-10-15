package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/supertx/qq-tavern/internal/config"
	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/model"
	"github.com/supertx/qq-tavern/internal/state"
)

type generateFunc func(context.Context, model.Request) (model.Response, error)

func (f generateFunc) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	return f(ctx, req)
}

type recordingSender struct {
	requests  []domain.SendRequest
	deadlines []time.Time
}

func (s *recordingSender) Send(ctx context.Context, req domain.SendRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d, _ := ctx.Deadline()
	s.requests = append(s.requests, req)
	s.deadlines = append(s.deadlines, d)
	return nil
}

func workerFixture(t *testing.T, generator Generator) (*Service, *state.Store, *recordingSender) {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sender := &recordingSender{}
	svc, err := New(config.Config{AppID: "test-app", Model: "test-model", SummaryModel: "summary-model", Mode: model.Responses, Effort: "high"}, store, generator, sender)
	if err != nil {
		t.Fatal(err)
	}
	return svc, store, sender
}

func workerEvent(id, group, user, text string) domain.Event {
	now := time.Now()
	return domain.Event{ID: id, MessageID: id, Scope: domain.Scope{BotID: domain.Key("test-app"), Kind: "group", ID: group}, UserID: user, UserName: "同名成员", Text: text, CreatedAt: now, Deadline: now.Add(config.TurnBudget)}
}

func handle(t *testing.T, svc *Service, event domain.Event) {
	t.Helper()
	if err := svc.Handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
}

func TestStandaloneCommandsAndChatWithoutControlPlane(t *testing.T) {
	var requests []model.Request
	svc, store, sender := workerFixture(t, generateFunc(func(_ context.Context, req model.Request) (model.Response, error) {
		requests = append(requests, req)
		return model.Response{Text: "普通聊天回答"}, nil
	}))
	handle(t, svc, workerEvent("character", "g1", "alice", "/角色 名称 丛雨"))
	handle(t, svc, workerEvent("model", "g1", "alice", "/当前模型"))
	if len(requests) != 0 || !strings.Contains(sender.requests[1].Text, "test-model") || !strings.Contains(sender.requests[1].Text, "high") {
		t.Fatal("commands invoked inference or omitted model settings")
	}
	e := workerEvent("chat", "g1", "alice", "今天聊聊普通的日常")
	handle(t, svc, e)
	handle(t, svc, e)
	if len(requests) != 1 || len(sender.requests) != 3 {
		t.Fatal("duplicate event caused duplicate model or send", len(requests), len(sender.requests))
	}
	if !strings.Contains(requests[0].System, "丛雨") || requests[0].Effort != "high" {
		t.Fatal(requests[0])
	}
	snapshot, err := store.Snapshot(context.Background(), e.Scope, e.UserID)
	if err != nil || len(snapshot.Config.Entries) != 0 || snapshot.Personal != "" {
		t.Fatal("ordinary chat wrote long-term memory", snapshot, err)
	}
	history, err := store.History(context.Background(), e.Scope, 20)
	if err != nil || len(history) != 1 {
		t.Fatal(history, err)
	}
	var messages []model.Message
	if err = json.Unmarshal(history[0].Payload, &messages); err != nil || len(messages) != 2 || messages[1].Content != "普通聊天回答" {
		t.Fatal(messages, err)
	}
}

func TestGroupMemberIdentityAndPersonalContextIsolation(t *testing.T) {
	var requests []model.Request
	svc, _, sender := workerFixture(t, generateFunc(func(_ context.Context, r model.Request) (model.Response, error) {
		requests = append(requests, r)
		return model.Response{Text: "答复"}, nil
	}))
	handle(t, svc, workerEvent("character", "g1", "alice", "/角色 名称 角色甲"))
	handle(t, svc, workerEvent("personal", "g1", "alice", "记住，回复我时用简短句子"))
	bob := workerEvent("bob", "g1", "bob", "相同昵称的另一个人")
	bob.Quotes = []domain.Quote{{AuthorName: "同名成员", Text: "无法确认引用作者"}}
	handle(t, svc, bob)
	if strings.Contains(requests[0].System, "简短句子") || !strings.Contains(requests[0].System, "角色甲") {
		t.Fatal("personal context leaked", requests[0].System)
	}
	if !strings.Contains(requests[0].Messages[0].Content, bob.MemberKey()) || !strings.Contains(requests[0].Messages[0].Content, "未知作者") {
		t.Fatal(requests[0].Messages)
	}
	alice := workerEvent("alice", "g1", "alice", "我也说一句")
	handle(t, svc, alice)
	r := requests[1]
	if !strings.Contains(r.System, "简短句子") || !strings.Contains(r.Messages[len(r.Messages)-1].Content, alice.MemberKey()) || alice.MemberKey() == bob.MemberKey() {
		t.Fatal("member identity or own memory lost")
	}
	other := workerEvent("other", "g2", "alice", "另一个群")
	handle(t, svc, other)
	if len(requests[2].Messages) != 1 || strings.Contains(requests[2].System, "角色甲") || strings.Contains(requests[2].System, "简短句子") {
		t.Fatal("cross-group history or memory leak")
	}
	wrong := workerEvent("foreign", "g1", "alice", "wrong bot")
	wrong.Scope.BotID = domain.Key("another-app")
	if err := svc.Handle(context.Background(), wrong); err == nil || len(sender.requests) != 5 {
		t.Fatal("accepted another bot's event", err)
	}
}

func TestToolLedgerDeduplicatesCallIDsAndArguments(t *testing.T) {
	svc, store, _ := workerFixture(t, generateFunc(func(context.Context, model.Request) (model.Response, error) {
		t.Fatal("unexpected generation")
		return model.Response{}, nil
	}))
	e := workerEvent("tools", "g1", "alice", "time")
	session := svc.tools.Session(e)
	call := model.Call{ID: "call1", Name: "current_time", Arguments: "{}"}
	first, err := svc.executeTool(context.Background(), session, call)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.executeTool(context.Background(), session, call)
	if err != nil || first != second || session.total != 1 {
		t.Fatal("same ID re-executed", err, session.total)
	}
	call.ID = "call2"
	third, err := svc.executeTool(context.Background(), session, call)
	if err != nil || third != first || session.counts["current_time"] != 1 {
		t.Fatal("same arguments re-executed", err)
	}
	call.ID = "call1"
	call.Arguments = `{"unexpected":true}`
	if _, err = svc.executeTool(context.Background(), session, call); err == nil {
		t.Fatal("same ID with changed arguments accepted")
	}
	_, err = store.DB().Exec(`INSERT INTO worker_tool(scope,event,call,name,args) VALUES(?,?,?,?,?)`, e.Scope.Key(), e.ID, "uncertain", "current_time", "{}")
	if err != nil {
		t.Fatal(err)
	}
	before := session.total
	result, err := svc.executeTool(context.Background(), session, model.Call{ID: "uncertain", Name: "current_time", Arguments: "{}"})
	if err != nil || !strings.Contains(result, "uncertain") || session.total != before {
		t.Fatal("uncertain tool execution was retried", result, err)
	}
}

func TestToolRoundTripPersistsRawItemsAndAccumulatesUsage(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	step := 0
	items := []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque-only","summary":[]}`), json.RawMessage(`{"type":"function_call","call_id":"first","name":"current_time","arguments":"{}"}`)}
	var replay model.Request
	svc, store, sender := workerFixture(t, generateFunc(func(_ context.Context, r model.Request) (model.Response, error) {
		step++
		switch step {
		case 1:
			return model.Response{Calls: []model.Call{{ID: "first", Name: "current_time", Arguments: "{}"}}, Items: items, Usage: model.Usage{InputTokens: 10, OutputTokens: 20, ReasoningTokens: 5}}, nil
		case 2:
			if len(r.Messages) != 3 || r.Messages[2].CallID != "first" || !bytes.Equal(r.Messages[1].Items[0], items[0]) {
				t.Error("tool output or raw reasoning lost", r.Messages)
			}
			return model.Response{Calls: []model.Call{{ID: "second", Name: "current_time", Arguments: "{}"}}, Usage: model.Usage{InputTokens: 30, OutputTokens: 40, ReasoningTokens: 10}}, nil
		case 3:
			if r.Messages[2].Content != r.Messages[4].Content {
				t.Error("duplicate tool arguments were not reused")
			}
			return model.Response{Text: "完成", Usage: model.Usage{InputTokens: 50, OutputTokens: 60, ReasoningTokens: 15}}, nil
		default:
			replay = r
			return model.Response{Text: "下一轮"}, nil
		}
	}))
	e := workerEvent("roundtrip", "g1", "alice", "告诉我时间")
	handle(t, svc, e)
	if len(sender.requests) != 1 || strings.Contains(sender.requests[0].Text, "opaque") {
		t.Fatal(sender.requests)
	}
	history, err := store.History(context.Background(), e.Scope, 20)
	if err != nil || len(history) != 1 {
		t.Fatal(history, err)
	}
	var messages []model.Message
	if err = json.Unmarshal(history[0].Payload, &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 6 || messages[1].SourceModel != "test-model" || messages[2].CallID != "first" || messages[4].CallID != "second" || !bytes.Equal(messages[1].Items[0], items[0]) {
		t.Fatal("incomplete stored tool turn", messages)
	}
	var record map[string]any
	if err = json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
		t.Fatal(err, logs.String())
	}
	if record["input_tokens"] != float64(90) || record["output_tokens"] != float64(120) || record["reasoning_tokens_subset"] != float64(30) {
		t.Fatal("tool round usage was lost or double-counted", record)
	}
	handle(t, svc, workerEvent("next", "g1", "bob", "然后呢"))
	if len(replay.Messages) != 7 || !bytes.Equal(replay.Messages[1].Items[0], items[0]) || replay.Messages[1].SourceModel != "test-model" {
		t.Fatal("raw replay lost", replay.Messages)
	}
}

func TestHistoryBudgetDropsCompleteToolTurns(t *testing.T) {
	var request model.Request
	svc, store, _ := workerFixture(t, generateFunc(func(_ context.Context, r model.Request) (model.Response, error) {
		request = r
		return model.Response{Text: "ok"}, nil
	}))
	scope := workerEvent("old", "g1", "alice", "old")
	old := []model.Message{{Role: "user", Content: "old-turn-start"}, {Role: "assistant", Calls: []model.Call{{ID: "old-call", Name: "current_time", Arguments: "{}"}}}, {Role: "tool", CallID: "old-call", Content: strings.Repeat("x", 60000)}, {Role: "assistant", Content: "old-turn-end"}}
	newer := []model.Message{{Role: "user", Content: "new-turn-start"}, {Role: "assistant", Calls: []model.Call{{ID: "new-call", Name: "current_time", Arguments: "{}"}}}, {Role: "tool", CallID: "new-call", Content: strings.Repeat("y", 50000)}, {Role: "assistant", Content: "new-turn-end"}}
	for i, turn := range [][]model.Message{old, newer} {
		e := scope
		e.ID = fmt.Sprintf("seed-%d", i)
		e.MessageID = e.ID
		data, _ := json.Marshal(turn)
		if err := store.AddTurn(context.Background(), e, "test-model", data); err != nil {
			t.Fatal(err)
		}
	}
	handle(t, svc, workerEvent("current", "g1", "alice", "新消息"))
	if len(request.Messages) != 5 || request.Messages[0].Content != "new-turn-start" || request.Messages[1].Calls[0].ID != "new-call" || request.Messages[2].CallID != "new-call" || request.Messages[3].Content != "new-turn-end" {
		t.Fatal("history was not pruned by whole tool turn", len(request.Messages))
	}
}

func TestEffectiveDeadlineReservesSendingTime(t *testing.T) {
	for _, parentLimit := range []bool{false, true} {
		t.Run(fmt.Sprint(parentLimit), func(t *testing.T) {
			var inferenceDeadline time.Time
			svc, _, sender := workerFixture(t, generateFunc(func(ctx context.Context, _ model.Request) (model.Response, error) {
				inferenceDeadline, _ = ctx.Deadline()
				return model.Response{Text: "ok"}, nil
			}))
			e := workerEvent("deadline", "g1", "alice", "hello")
			e.CreatedAt = time.Now().Add(-250 * time.Second)
			e.Deadline = time.Now().Add(20 * time.Minute)
			ctx := context.Background()
			expected := e.CreatedAt.Add(config.TurnBudget)
			if parentLimit {
				var cancel context.CancelFunc
				expected = time.Now().Add(20 * time.Second)
				ctx, cancel = context.WithDeadline(ctx, expected)
				defer cancel()
			}
			if err := svc.Handle(ctx, e); err != nil {
				t.Fatal(err)
			}
			if !inferenceDeadline.Equal(expected.Add(-10*time.Second)) || len(sender.deadlines) != 1 || !sender.deadlines[0].Equal(expected) {
				t.Fatal("send reserve lost", inferenceDeadline, expected, sender.deadlines)
			}
		})
	}
}

func TestSummaryTriggersAndFailureKeepsCheckpoint(t *testing.T) {
	var summaryInputs []model.Request
	failSummary := false
	svc, store, _ := workerFixture(t, generateFunc(func(_ context.Context, r model.Request) (model.Response, error) {
		if r.Model == "summary-model" {
			summaryInputs = append(summaryInputs, r)
			if failSummary {
				return model.Response{}, errors.New("summary unavailable")
			}
			return model.Response{Text: "可靠的原摘要"}, nil
		}
		return model.Response{Text: "聊天回复"}, nil
	}))
	ctx := context.Background()
	e := workerEvent("one", "g1", "alice", "第一轮")
	handle(t, svc, e)
	due, err := store.DueSummaries(ctx, time.Now())
	if err != nil || len(due) != 0 {
		t.Fatal(due, err)
	}
	due, err = store.DueSummaries(ctx, time.Now().Add(121*time.Second))
	if err != nil || len(due) != 1 || due[0] != e.Scope {
		t.Fatal("idle summary not due", due, err)
	}
	handle(t, svc, workerEvent("two", "g1", "bob", "第二轮"))
	due, err = store.DueSummaries(ctx, time.Now())
	if err != nil || len(due) != 0 {
		t.Fatal("summary ran before three turns", due, err)
	}
	handle(t, svc, workerEvent("three", "g1", "alice", "第三轮"))
	due, err = store.DueSummaries(ctx, time.Now())
	if err != nil || len(due) != 1 {
		t.Fatal("summary not due after three turns", due, err)
	}
	if err = svc.summarize(ctx, e.Scope); err != nil {
		t.Fatal(err)
	}
	if len(summaryInputs) != 1 || summaryInputs[0].Effort != "medium" || len(summaryInputs[0].Tools) != 0 || !strings.Contains(summaryInputs[0].Messages[0].Content, e.MemberKey()) {
		t.Fatal("summary lost identities or used wrong model configuration")
	}
	old, err := store.Summary(ctx, e.Scope)
	if err != nil || old.Checkpoint == 0 || old.Text != "可靠的原摘要" {
		t.Fatal(old, err)
	}
	due, err = store.DueSummaries(ctx, time.Now().Add(time.Hour))
	if err != nil || len(due) != 0 {
		t.Fatal("completed summary remains due", due, err)
	}
	handle(t, svc, workerEvent("four", "g1", "alice", "新的一轮"))
	failSummary = true
	if err = svc.summarize(ctx, e.Scope); err == nil {
		t.Fatal("expected summary failure")
	}
	current, err := store.Summary(ctx, e.Scope)
	if err != nil || current != old {
		t.Fatal("failed summary overwrote old state", current, old, err)
	}
	snap, err := store.Snapshot(ctx, e.Scope, e.UserID)
	if err != nil || len(snap.Config.Entries) != 0 || snap.Personal != "" {
		t.Fatal("summaries extracted long-term facts", snap, err)
	}
}

func TestSettingsSnapshotChangesOnlyNextTurn(t *testing.T) {
	var svc *Service
	var firstSystem string
	step := 0
	generator := generateFunc(func(_ context.Context, r model.Request) (model.Response, error) {
		step++
		switch step {
		case 1:
			firstSystem = r.System
			_, err := svc.commands.Handle(context.Background(), workerEvent("edit-during-turn", "g1", "bob", "/角色 名称 新角色"))
			if err != nil {
				t.Fatal(err)
			}
			return model.Response{Calls: []model.Call{{ID: "time", Name: "current_time", Arguments: "{}"}}}, nil
		case 2:
			if r.System != firstSystem {
				t.Fatal("active tool turn changed configuration snapshot")
			}
		case 3:
			if !strings.Contains(r.System, "新角色") {
				t.Fatal("next turn did not load new configuration")
			}
		}
		return model.Response{Text: "完成"}, nil
	})
	svc, _, _ = workerFixture(t, generator)
	handle(t, svc, workerEvent("first-turn", "g1", "alice", "开始"))
	handle(t, svc, workerEvent("second-turn", "g1", "alice", "继续"))
}

func TestRefusalAndFailedEventAreNotReplayed(t *testing.T) {
	calls := 0
	svc, store, sender := workerFixture(t, generateFunc(func(context.Context, model.Request) (model.Response, error) {
		calls++
		return model.Response{Refusal: "无法协助这项请求。"}, &model.Error{Kind: "refusal", Message: "provider declined"}
	}))
	e := workerEvent("refusal", "g1", "alice", "问题")
	if err := svc.Handle(context.Background(), e); err == nil {
		t.Fatal("expected refusal error for observability")
	}
	handle(t, svc, e)
	if calls != 1 || len(sender.requests) != 1 || sender.requests[0].Text != "无法协助这项请求。" {
		t.Fatal("refusal was hidden or failed event was retried", calls, sender.requests)
	}
	var status string
	if err := store.DB().QueryRow(`SELECT status FROM worker_event WHERE scope=? AND event=?`, e.Scope.Key(), e.ID).Scan(&status); err != nil || status != "failed" {
		t.Fatal(status, err)
	}
	e.ID = "expired"
	e.Deadline = time.Now().Add(-time.Second)
	handle(t, svc, e)
	if calls != 1 || len(sender.requests) != 1 {
		t.Fatal("expired queue event produced an unsolicited reply")
	}
}

func TestSummaryExcludesOpaqueReasoningAndPreservesMemberLabels(t *testing.T) {
	var summaryRequest model.Request
	svc, store, _ := workerFixture(t, generateFunc(func(_ context.Context, req model.Request) (model.Response, error) {
		summaryRequest = req
		return model.Response{Text: "可见内容摘要"}, nil
	}))
	e := workerEvent("seed", "g1", "alice", "可见问题")
	turn := []model.Message{userMessage(e), {Role: "assistant", Content: "可见回答", Items: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque-never-summarize","summary":[{"type":"summary_text","text":"internal-never-summarize"}]}`)}}}
	payload, _ := json.Marshal(turn)
	if err := store.AddTurn(context.Background(), e, "test-model", payload); err != nil {
		t.Fatal(err)
	}
	if err := svc.summarize(context.Background(), e.Scope); err != nil {
		t.Fatal(err)
	}
	input := summaryRequest.Messages[0].Content
	if strings.Contains(input, "never-summarize") || !strings.Contains(input, e.MemberKey()) || !strings.Contains(input, "可见回答") {
		t.Fatal(input)
	}
}

func TestSummaryUsesConfiguredEffortAndModel(t *testing.T) {
	svc, store, _ := workerFixture(t, generateFunc(func(_ context.Context, r model.Request) (model.Response, error) {
		if r.Effort != "high" || r.Model != "glm-5.3" {
			t.Fatal(r.Effort, r.Model)
		}
		return model.Response{Text: "合成摘要"}, nil
	}))
	svc.cfg.SummaryEffort = "high"
	svc.cfg.SummaryModel = "glm-5.3"
	e := workerEvent("summary-effort", "group", "alice", "合成测试消息")
	payload, _ := json.Marshal([]model.Message{{Role: "user", Content: "合成测试消息"}, {Role: "assistant", Content: "回复"}})
	if err := store.AddTurn(context.Background(), e, "glm-5.3-flash", payload); err != nil {
		t.Fatal(err)
	}
	if err := svc.summarize(context.Background(), e.Scope); err != nil {
		t.Fatal(err)
	}
}

func TestFailureLogsStatusWithoutProviderContents(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)
	err := fmt.Errorf("wrapped: %w", &model.Error{Kind: "http", StatusCode: 405, Message: "sensitive-provider-body"})
	logFailure("turn failed", err, "model", "test-model", "protocol", "responses")
	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["error_kind"] != "http" || record["http_status"] != float64(405) || record["protocol"] != "responses" {
		t.Fatal(record)
	}
	if strings.Contains(buf.String(), "sensitive-provider-body") {
		t.Fatal("provider content leaked")
	}
}
