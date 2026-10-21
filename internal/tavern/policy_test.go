package tavern

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/state"
)

func TestDefaultClosedGroupsAndReadOnlyCommands(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "policy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	svc := New(s)
	e := domain.Event{Scope: domain.Scope{BotID: "bot", Kind: "group", ID: "g"}, UserID: "alice"}
	for i, command := range []string{
		"/角色 名称 A", "/角色 新建 B", "/角色 切换 B", "/角色 清空",
		"/世界书 添加 常驻 世界 | 内容", "/群规则 设置 规则",
		"/记到群 事实", "/记住 事实", "记住，以后每次回复加尾巴",
		"/记忆 设置 偏好", "记住，我喜欢猫",
		"/状态设置 好感度 99", "/状态重置 好感度", "/状态项 停用 好感度",
		"/摘要 更新", "/摘要 清空", "/重置上下文",
	} {
		e.ID, e.Text = fmt.Sprint(i), command
		r, err := svc.Handle(context.Background(), e)
		if err != nil || !r.Handled || !strings.Contains(r.Text, "未保存") || r.RequestSummary {
			t.Fatalf("%s: %+v %v", command, r, err)
		}
	}
	for i, command := range []string{"/酒馆", "/角色", "/角色 列表", "/世界书", "/群规则", "/记忆", "/摘要", "/角色状态", "/状态项", "/会话信息"} {
		e.ID, e.Text = fmt.Sprint("read", i), command
		r, err := svc.Handle(context.Background(), e)
		if err != nil || !r.Handled || strings.Contains(r.Text, "未保存") {
			t.Fatalf("%s: %+v %v", command, r, err)
		}
	}
	_, err = s.SetPolicy(context.Background(), 0, map[state.CommandGroup]bool{state.PersonalMemory: true, state.RoleEdit: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, scope := range []domain.Scope{e.Scope, {BotID: "bot", Kind: "group", ID: "other"}, {BotID: "bot", Kind: "private", ID: "alice"}} {
		e.Scope, e.ID, e.Text = scope, fmt.Sprint("personal", i), "记住，我喜欢猫"
		r, err := svc.Handle(context.Background(), e)
		if err != nil || !strings.Contains(r.Text, "已保存") {
			t.Fatal(r, err)
		}
	}
	e.Scope = domain.Scope{BotID: "bot", Kind: "group", ID: "g"}
	e.ID, e.Text = "create", "/角色 新建 B"
	r, err := svc.Handle(context.Background(), e)
	if err != nil || !strings.Contains(r.Text, "已创建") {
		t.Fatal(r, err)
	}
	active, err := s.BindActive(context.Background(), e.Scope)
	if err != nil || active.Role().Name == "B" {
		t.Fatal("creation bypassed disabled switch group")
	}
	e.ID, e.Text = "world-denied", "记住，公共事实"
	r, err = svc.Handle(context.Background(), e)
	if err != nil || !strings.Contains(r.Text, "未保存") {
		t.Fatal("personal memory granted public write", r, err)
	}
}

func TestEachGroupCanBeOpenedIndependently(t *testing.T) {
	cases := []struct {
		group   state.CommandGroup
		command string
	}{
		{state.RoleEdit, "/角色 描述 新人设"},
		{state.RoleSwitch, "/角色 切换 另一角色"},
		{state.WorldEdit, "/群规则 设置 新规则"},
		{state.PersonalMemory, "/记忆 设置 个人偏好"},
		{state.StatusManage, "/状态设置 好感度 30"},
		{state.ContextManage, "/摘要 设置 手动摘要"},
	}
	for _, tc := range cases {
		t.Run(string(tc.group), func(t *testing.T) {
			ctx := context.Background()
			s, err := state.Open(filepath.Join(t.TempDir(), "policy.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			e := domain.Event{Scope: domain.Scope{BotID: "bot", Kind: "group", ID: "g"}, UserID: "alice"}
			if _, err = s.AdminCreateRole(ctx, e.Scope, "另一角色"); err != nil {
				t.Fatal(err)
			}
			if _, err = s.SetPolicy(ctx, 0, map[state.CommandGroup]bool{tc.group: true}); err != nil {
				t.Fatal(err)
			}
			svc := New(s)
			for i, command := range cases {
				e.ID, e.Text = fmt.Sprint(i), command.command
				r, err := svc.Handle(ctx, e)
				if err != nil || !r.Handled {
					t.Fatal(r, err)
				}
				denied := strings.Contains(r.Text, "未保存")
				if denied == (command.group == tc.group) {
					t.Fatalf("opening %s, command %s: %s", tc.group, command.command, r.Text)
				}
			}
		})
	}
}
