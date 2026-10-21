package tavern

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/state"
)

func TestRememberCommandsAndOrdinaryChat(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	enableCommands(t, s)
	service := New(s)
	ctx := context.Background()
	e := domain.Event{ID: "one", MessageID: "one", Scope: domain.Scope{BotID: "bot", Kind: "group", ID: "group"}, UserID: "alice", UserName: "同名", Deadline: time.Now().Add(time.Minute)}
	run := func(id, text string) Result {
		t.Helper()
		e.ID = id
		e.Text = text
		result, err := service.Handle(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := run("ordinary", "今天讨论了咖啡，我记得昨天也讨论过"); result.Handled {
		t.Fatal("ordinary chat treated as a persistent memory request")
	}
	if result := run("recall", "记住了吗？"); result.Handled {
		t.Fatal("recall question wrote a new memory")
	}
	if result := run("fact", "记住，这个群每周五举办读书会"); !result.Handled {
		t.Fatal("explicit request not handled")
	}
	if result := run("rule", "记住，以后每次对话完成都加一句，收到"); !result.Handled {
		t.Fatal("rule not handled")
	}
	if result := run("personal", "记住，回复我时用简短句子"); !result.Handled {
		t.Fatal("personal not handled")
	}
	run("personal", "记住，回复我时用简短句子")
	snap, _ := s.Snapshot(ctx, e.Scope, e.UserID)
	if len(snap.Config.Entries) != 1 || !strings.Contains(snap.Config.Note, "收到") || strings.Count(snap.Personal, "简短句子") != 1 {
		t.Fatalf("bad memories: %+v", snap)
	}
	e.UserID = "bob"
	run("character", "/角色 名称 丛雨")
	snap, _ = s.Snapshot(ctx, e.Scope, e.UserID)
	if snap.Config.Character.Name != "丛雨" || snap.Personal != "" {
		t.Fatalf("public edit or private isolation: %+v", snap)
	}
	e.Scope = domain.Scope{BotID: "bot", Kind: "private", ID: "bob"}
	result := run("cross", "/记到群 其他群记忆")
	if !strings.Contains(result.Text, "私聊不能修改群记忆") {
		t.Fatal(result.Text)
	}
	run("private", "/记住 我喜欢猫")
	snap, _ = s.Snapshot(ctx, e.Scope, e.UserID)
	if !strings.Contains(snap.Personal, "喜欢猫") || len(snap.Config.Entries) != 0 {
		t.Fatal("private request wrote public data")
	}
}

func TestWorldbookCommandsAndActivationPersistence(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	enableCommands(t, s)
	svc := New(s)
	ctx := context.Background()
	e := domain.Event{Scope: domain.Scope{BotID: "bot", Kind: "group", ID: "group"}, UserID: "alice", Deadline: time.Now().Add(time.Minute)}
	commands := []string{"/世界书 添加 关键词 苹果,梨 | 水果 | 她喜欢水果", "/世界书 优先级 1 100", "/世界书 预算 100", "/世界书 修改 1 群内讨论水果", "/角色 场景 果园"}
	for i, command := range commands {
		e.ID = command
		e.Text = command
		result, err := svc.Handle(ctx, e)
		if err != nil || !result.Handled {
			t.Fatalf("command %d: %+v %v", i, result, err)
		}
	}
	snapshot, err := s.Snapshot(ctx, e.Scope, e.UserID)
	if err != nil {
		t.Fatal(err)
	}
	prompt, status := BuildContext(snapshot, "苹果成熟了吗")
	if !strings.Contains(prompt, "群内讨论水果") || len(status) != 1 || !status[0].Injected {
		t.Fatal(prompt, status)
	}
	if err = s.RecordActivation(ctx, e, status); err != nil {
		t.Fatal(err)
	}
	e.ID = "status"
	e.Text = "/记忆状态"
	result, err := svc.Handle(ctx, e)
	if err != nil || !strings.Contains(result.Text, "已注入") {
		t.Fatal(result, err)
	}
	e.ID = "invalid"
	e.Text = "/世界书 优先级 1 101"
	result, err = svc.Handle(ctx, e)
	if err != nil || !strings.Contains(result.Text, "0–100") {
		t.Fatal(result, err)
	}
	e.ID = "empty-page"
	e.Text = "/世界书 列表 999"
	result, err = svc.Handle(ctx, e)
	if err != nil || !strings.Contains(result.Text, "仅有") {
		t.Fatal(result, err)
	}
}

func enableCommands(t *testing.T, s *state.Store) {
	t.Helper()
	groups := map[state.CommandGroup]bool{}
	for _, g := range state.CommandGroups() {
		groups[g.ID] = true
	}
	if _, err := s.SetPolicy(context.Background(), 0, groups); err != nil {
		t.Fatal(err)
	}
}
func TestActivationChinesePriorityAndBudget(t *testing.T) {
	snap := state.Snapshot{Config: state.Config{Budget: 40, Entries: []state.Entry{
		{ID: 1, Title: "中国", Content: "中国节日", Keywords: []string{"节假日"}, Enabled: true, Priority: 100},
		{ID: 2, Title: "关闭", Content: "secret", Constant: true, Enabled: false},
		{ID: 3, Title: "未命中", Content: "other", Keywords: []string{"完全不存在"}, Enabled: true},
		{ID: 4, Title: "预算", Content: strings.Repeat("长", 60), Constant: true, Enabled: true},
	}}, Personal: "我是当前用户", Summary: state.Summary{Text: "旧摘要"}}
	text, statuses := BuildContext(snap, "明天节假日怎么安排")
	if !strings.Contains(text, "中国节日") || strings.Contains(text, "secret") || strings.Contains(text, "other") || !strings.Contains(text, "旧摘要") {
		t.Fatal(text)
	}
	results := map[int]state.Activation{}
	for _, a := range statuses {
		results[a.ID] = a
	}
	if !results[1].Injected || results[2].Reason != "已保存，已停用" || results[3].Reason != "已保存，关键词未命中" || results[4].Reason != "已保存，字符预算不足" {
		t.Fatalf("%+v", results)
	}
}
