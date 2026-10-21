package tavern

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/state"
)

const statusHelp = `/角色 列表；/角色 新建 名称；/角色 切换 名称（群内全员切换）
/角色状态 [角色名]；/好感度 [角色名]；/状态记录
/状态设置 项目 值；/状态重置 项目
/状态项：查看定义与自定义语法
普通回复不附加状态栏。成员状态只查询或修改自己的部分。`

func (s *Service) roleStatusCommand(ctx context.Context, e domain.Event, command, args string, snap state.Snapshot) (Result, bool, error) {
	switch command {
	case "/角色":
		action, name := splitCommand(args)
		switch action {
		case "列表":
			roles, err := s.store.Roles(ctx, e.Scope)
			if err != nil {
				return Result{}, true, err
			}
			lines := []string{"本会话角色："}
			for _, r := range roles {
				suffix := ""
				if r.Active {
					suffix = "（当前）"
				}
				lines = append(lines, r.Name+suffix)
			}
			return reply(strings.Join(lines, "\n")), true, nil
		case "新建", "切换":
			if name == "" {
				return reply("用法：/角色 " + action + " 名称"), true, nil
			}
			err := s.store.SelectRole(ctx, e, name, action == "新建")
			if action == "新建" {
				return reply("已创建角色「" + name + "」。使用 /角色 切换 " + name + " 切换（需要角色切换权限）。"), true, err
			}
			return reply("当前角色已切换为「" + name + "」，本会话所有成员生效。"), true, err
		}
		return Result{}, false, nil
	case "/角色状态", "/好感度":
		view := s.store
		if args != "" {
			var err error
			view, err = s.store.BindRole(ctx, e.Scope, args)
			if err != nil {
				return Result{}, true, err
			}
			snap, err = view.Snapshot(ctx, e.Scope, e.UserID)
			if err != nil {
				return Result{}, true, err
			}
		}
		name := "默认角色"
		if snap.Role != nil {
			name = snap.Role.Name
		}
		lines := []string{name + " · 当前状态（成员项为对你）"}
		for _, d := range snap.Status.Schema.Definitions {
			if !d.Enabled || (command == "/好感度" && d.Name != "好感度") {
				continue
			}
			owner := "对你"
			if d.Owner == "role" {
				owner = "共享"
			}
			lines = append(lines, fmt.Sprintf("%s：%v（%s）", d.Name, snap.Status.Value(d), owner))
		}
		if len(lines) == 1 {
			lines = append(lines, "没有符合条件的已启用状态项。")
		}
		records, err := view.StatusRecords(ctx, e)
		if err != nil {
			return Result{}, true, err
		}
		if len(records) > 0 {
			lines = append(lines, "最近变化："+records[0].Reason)
		} else {
			lines = append(lines, "尚无变化记录，当前使用初始值。")
		}
		return reply(strings.Join(lines, "\n")), true, nil
	case "/状态记录":
		records, err := s.store.StatusRecords(ctx, e)
		if err != nil {
			return Result{}, true, err
		}
		lines := []string{"当前角色最近的状态变化："}
		for _, r := range records {
			keys := []string{}
			for k, v := range r.After.Values {
				if !reflect.DeepEqual(r.Before.Values[k], v) {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			changes := []string{}
			for _, k := range keys {
				changes = append(changes, fmt.Sprintf("%s：%v → %v", k, r.Before.Values[k], r.After.Values[k]))
			}
			owner := "对你"
			if r.Shared {
				owner = "共享"
			}
			lines = append(lines, fmt.Sprintf("#%d %s（%s）%s\n原因：%s", r.ID, r.CreatedAt.Local().Format("01-02 15:04"), owner, strings.Join(changes, "；"), r.Reason))
		}
		if len(records) == 0 {
			lines = append(lines, "暂无变化记录。")
		}
		return reply(strings.Join(lines, "\n")), true, nil
	case "/状态设置", "/状态重置":
		name, value := splitCommand(args)
		d, ok := snap.Status.Definition(name)
		if !ok {
			return reply("状态项不存在，请使用 /状态项 查看。"), true, nil
		}
		reset := command == "/状态重置"
		var v any = value
		if !reset && d.Kind == "number" {
			n, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return reply("请输入有效数值，例如 /状态设置 好感度 30"), true, nil
			}
			v = n
		}
		if reset && value != "" {
			return reply("用法：/状态重置 项目"), true, nil
		}
		err := s.store.SetStatus(ctx, e, name, v, reset)
		return reply("已保存「" + name + "」。"), true, err
	case "/状态项":
		result, err := s.statusDefinitions(ctx, e, args, snap.Status)
		return result, true, err
	}
	return Result{}, false, nil
}

func (s *Service) statusDefinitions(ctx context.Context, e domain.Event, args string, snapshot state.StatusSnapshot) (Result, error) {
	if args == "" {
		lines := []string{"当前角色的状态项："}
		for _, d := range snapshot.Schema.Definitions {
			kind, owner, enabled := "数值", "成员独立", "启用"
			if d.Kind == "text" {
				kind = "文本"
			}
			if d.Owner == "role" {
				owner = "角色共享"
			}
			if !d.Enabled {
				enabled = "停用"
			}
			line := fmt.Sprintf("%s：%s／%s／%s；初始 %v", d.Name, kind, owner, enabled, d.Initial)
			if d.Kind == "number" {
				line += fmt.Sprintf("；范围 %g～%g；单轮 ±%g", d.Min, d.Max, d.MaxDelta)
			}
			if d.Rule != "" {
				line += "\n规则：" + d.Rule
			}
			lines = append(lines, line)
		}
		lines = append(lines, "/状态项 添加 名称 数值|文本 成员|角色\n/状态项 设置 名称 初始值|范围|单轮上限|规则 内容\n/状态项 启用|停用 名称\n例：/状态项 设置 信任度 范围 0 100\n初始值修改不覆盖已保存值；停用保留数据。")
		return reply(strings.Join(lines, "\n")), nil
	}
	action, rest := splitCommand(args)
	name, rest := splitCommand(rest)
	err := s.store.UpdateStatusSchema(ctx, e, snapshot.Schema.Version, func(schema *state.StatusSchema) error {
		index := -1
		for i, d := range schema.Definitions {
			if d.Name == name {
				index = i
				break
			}
		}
		if action == "添加" {
			if index >= 0 {
				return errors.New("已有同名状态项")
			}
			fields := strings.Fields(rest)
			if len(fields) != 2 {
				return errors.New("用法：/状态项 添加 名称 数值|文本 成员|角色")
			}
			d := state.StatusDefinition{Name: name, Kind: "number", Owner: "member", Initial: float64(0), Min: 0, Max: 100, MaxDelta: 3, Enabled: true}
			switch fields[0] {
			case "数值":
			case "文本":
				d.Kind = "text"
				d.Initial = "未设定"
			default:
				return errors.New("类型须为数值或文本")
			}
			switch fields[1] {
			case "成员":
			case "角色":
				d.Owner = "role"
			default:
				return errors.New("归属须为成员或角色")
			}
			schema.Definitions = append(schema.Definitions, d)
			return nil
		}
		if index < 0 {
			return errors.New("状态项不存在")
		}
		d := &schema.Definitions[index]
		switch action {
		case "启用", "停用":
			if rest != "" {
				return errors.New("用法：/状态项 启用|停用 名称")
			}
			d.Enabled = action == "启用"
		case "设置":
			field, value := splitCommand(rest)
			switch field {
			case "规则":
				d.Rule = value
			case "初始值":
				if d.Kind == "text" {
					d.Initial = value
				} else {
					n, err := strconv.ParseFloat(value, 64)
					if err != nil {
						return errors.New("初始值须为数值")
					}
					d.Initial = n
				}
			case "范围":
				if d.Kind != "number" {
					return errors.New("文本项没有数值范围")
				}
				f := strings.Fields(value)
				if len(f) != 2 {
					return errors.New("用法：/状态项 设置 名称 范围 最小值 最大值")
				}
				min, e1 := strconv.ParseFloat(f[0], 64)
				max, e2 := strconv.ParseFloat(f[1], 64)
				if e1 != nil || e2 != nil {
					return errors.New("范围须为两个数值")
				}
				d.Min = min
				d.Max = max
			case "单轮上限":
				if d.Kind != "number" {
					return errors.New("文本项没有单轮数值上限")
				}
				n, err := strconv.ParseFloat(value, 64)
				if err != nil {
					return errors.New("单轮上限须为正数")
				}
				d.MaxDelta = n
			default:
				return errors.New("可设置：初始值、范围、单轮上限、规则")
			}
		default:
			return errors.New("用法：/状态项 添加|设置|启用|停用 …")
		}
		return nil
	})
	return reply("状态项配置已保存。"), err
}
