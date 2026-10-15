package tavern

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/state"
)

const worldbookHelp = `世界书语法：
/世界书 列表 [页码]（每页 10 条）
/世界书 添加 常驻 标题 | 内容
/世界书 添加 关键词 关键词1,关键词2 | 标题 | 内容
/世界书 查看 编号
/世界书 修改 编号 新内容
/世界书 开启|关闭|删除 编号
/世界书 优先级 编号 0–100
/世界书 关键词 编号 词1,词2（或：常驻）
/世界书 预算 0–24000（字符，非 token）
/世界书 清空 确认`

func listEntries(entries []state.Entry, full bool) string {
	if len(entries) == 0 {
		return "（空）"
	}
	lines := []string{}
	for _, e := range entries {
		mode := "常驻"
		if !e.Constant {
			mode = "关键词：" + strings.Join(e.Keywords, ",")
		}
		enabled := "开启"
		if !e.Enabled {
			enabled = "关闭"
		}
		line := fmt.Sprintf("#%d %s [%s；%s；优先级 %d]", e.ID, e.Title, enabled, mode, e.Priority)
		if full {
			line += "\n" + e.Content
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
func keywords(value string) []string {
	items := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '，' })
	out := []string{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
func (s *Service) worldbook(ctx context.Context, e domain.Event, args string, snap state.Snapshot) (Result, error) {
	action, rest := splitCommand(args)
	if args == "" || action == "列表" {
		page := 1
		if rest != "" {
			var err error
			page, err = strconv.Atoi(rest)
			if err != nil || page < 1 {
				return reply("用法：/世界书 列表 页码（从 1 开始）"), nil
			}
		}
		pages := (len(snap.Config.Entries) + 9) / 10
		if pages == 0 {
			pages = 1
		}
		if page > pages {
			return reply(fmt.Sprintf("仅有 %d 页。", pages)), nil
		}
		start := (page - 1) * 10
		end := start + 10
		if end > len(snap.Config.Entries) {
			end = len(snap.Config.Entries)
		}
		return reply(fmt.Sprintf("世界书 第 %d/%d 页\n%s\n\n%s", page, pages, listEntries(snap.Config.Entries[start:end], false), worldbookHelp)), nil
	}
	if action == "查看" {
		id, err := strconv.Atoi(rest)
		if err == nil {
			for _, entry := range snap.Config.Entries {
				if entry.ID == id {
					return reply(listEntries([]state.Entry{entry}, true)), nil
				}
			}
		}
		return reply("没有该条目。"), nil
	}
	var mutate func(*state.Config) error
	switch action {
	case "添加":
		mode, content := splitCommand(rest)
		parts := strings.Split(content, "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		entry := state.Entry{Enabled: true, Priority: 50}
		switch {
		case mode == "常驻" && len(parts) == 2:
			entry.Constant = true
			entry.Title = parts[0]
			entry.Content = parts[1]
		case mode == "关键词" && len(parts) == 3:
			entry.Keywords = keywords(parts[0])
			entry.Title = parts[1]
			entry.Content = parts[2]
		default:
			return reply(worldbookHelp), nil
		}
		mutate = func(c *state.Config) error {
			entry.ID = 1
			for _, old := range c.Entries {
				if old.ID >= entry.ID {
					entry.ID = old.ID + 1
				}
			}
			c.Entries = append(c.Entries, entry)
			return nil
		}
	case "预算":
		n, err := strconv.Atoi(rest)
		if err != nil || n < 0 || n > 24000 {
			return reply("世界书预算必须为 0–24000 字符。"), nil
		}
		mutate = func(c *state.Config) error { c.Budget = n; return nil }
	case "清空":
		if rest != "确认" {
			return reply("删除本会话全部世界书条目请使用：/世界书 清空 确认。可通过修改记录撤销。"), nil
		}
		mutate = func(c *state.Config) error { c.Entries = []state.Entry{}; return nil }
	case "修改", "开启", "关闭", "删除", "优先级", "关键词":
		idText, value := splitCommand(rest)
		id, err := strconv.Atoi(idText)
		if err != nil || id < 1 {
			return reply(worldbookHelp), nil
		}
		if (action == "修改" || action == "关键词") && value == "" {
			return reply(worldbookHelp), nil
		}
		priority := 0
		if action == "优先级" {
			priority, err = strconv.Atoi(value)
			if err != nil || priority < 0 || priority > 100 {
				return reply("优先级必须为 0–100。"), nil
			}
		}
		mutate = func(c *state.Config) error {
			for i := range c.Entries {
				entry := &c.Entries[i]
				if entry.ID != id {
					continue
				}
				switch action {
				case "修改":
					entry.Content = value
				case "开启":
					entry.Enabled = true
				case "关闭":
					entry.Enabled = false
				case "删除":
					c.Entries = append(c.Entries[:i], c.Entries[i+1:]...)
				case "优先级":
					entry.Priority = priority
				case "关键词":
					entry.Constant = value == "常驻"
					entry.Keywords = nil
					if !entry.Constant {
						entry.Keywords = keywords(value)
					}
				}
				return nil
			}
			return state.ErrNotFound
		}
	default:
		return reply(worldbookHelp), nil
	}
	_, err := s.store.UpdateConfig(ctx, e, snap.Config.Version, mutate)
	return reply("世界书已保存，下一轮对话生效。用 /记忆状态 查看最近实际注入情况。"), err
}
