package tavern

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/state"
)

type Service struct{ store *state.Store }
type Result struct {
	Store          *state.Store
	Handled        bool
	Text           string
	RequestSummary bool
}

func New(store *state.Store) *Service { return &Service{store: store} }

const help = `酒馆设置只影响当前会话，群成员均可修改群公共设置。
/角色 [名称|描述|性格|场景 内容]
/群规则 [设置|添加 内容]，/群规则 清空
/世界书：查看条目与编辑语法
/摘要 [更新|设置 内容|清空]
/记住 内容：群内默认保存到公共世界书
/记到群 内容：保存本群公共事实
/记忆 [设置 内容|清空]：仅自己的记忆
/群记忆、/记忆状态、/修改记录、/撤销修改 编号
/重置上下文：清除聊天历史和摘要，保留长期记忆
/当前模型：查看模型与思考强度
世界书预算按字符估算；记住不代表每轮都会注入。`

func splitCommand(text string) (string, string) {
	p := strings.IndexAny(text, " \t\n")
	if p < 0 {
		return text, ""
	}
	return text[:p], strings.TrimSpace(text[p:])
}
func reply(text string) Result { return Result{Handled: true, Text: text} }
func shown(text string) string {
	if text == "" {
		return "（空）"
	}
	return text
}
func cut(text string, n int) string {
	r := []rune(text)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return text
}

func (s *Service) Handle(ctx context.Context, e domain.Event) (Result, error) {
	text := strings.TrimSpace(e.Text)
	command, args := splitCommand(text)
	known := map[string]bool{"/角色状态": true, "/好感度": true, "/状态记录": true, "/状态设置": true, "/状态重置": true, "/状态项": true, "/酒馆": true, "/角色": true, "/群规则": true, "/世界书": true, "/摘要": true, "/记住": true, "/记到群": true, "/记忆": true, "/群记忆": true, "/记忆状态": true, "/修改记录": true, "/撤销修改": true, "/重置上下文": true}
	natural, explicit := explicitMemory(text)
	if !known[command] && !explicit {
		return Result{}, nil
	}
	bound, err := s.store.BindActive(ctx, e.Scope)
	if err != nil {
		return Result{}, err
	}
	s = New(bound)
	snap, err := s.store.Snapshot(ctx, e.Scope, e.UserID)
	if err != nil {
		return Result{}, err
	}
	result, err := s.handle(ctx, e, command, args, natural, explicit, snap)
	if errors.Is(err, state.ErrConflict) {
		return reply("设置已被其他成员或后台更新，请查看最新内容后重试。"), nil
	}
	if errors.Is(err, state.ErrNotFound) {
		return reply("当前会话没有这条记录，或你无权操作它。"), nil
	}
	result.Store = s.store
	return result, err
}
func (s *Service) handle(ctx context.Context, e domain.Event, command, args, natural string, explicit bool, snap state.Snapshot) (Result, error) {
	if result, handled, err := s.roleStatusCommand(ctx, e, command, args, snap); handled {
		return result, err
	}
	update := func(change func(*state.Config) error) (Result, error) {
		_, err := s.store.UpdateConfig(ctx, e, snap.Config.Version, change)
		if err != nil {
			return reply("保存失败：" + err.Error()), err
		}
		return reply("已保存到当前会话，下一轮对话生效。"), nil
	}
	switch command {
	case "/酒馆":
		return reply(fmt.Sprintf("%s\n当前角色：%s；设置版本：%d；世界书：%d 条。", help+"\n"+statusHelp, shown(snap.Config.Character.Name), snap.Config.Version, len(snap.Config.Entries))), nil
	case "/角色":
		if args == "" {
			c := snap.Config.Character
			return reply(fmt.Sprintf("角色（版本 %d）\n名称：%s\n描述：%s\n性格：%s\n场景：%s", snap.Config.Version, shown(c.Name), shown(c.Description), shown(c.Personality), shown(c.Scenario))), nil
		}
		field, value := splitCommand(args)
		if value == "" && field != "清空" {
			return reply("用法：/角色 名称|描述|性格|场景 内容，或 /角色 清空"), nil
		}
		if field != "名称" && field != "描述" && field != "性格" && field != "场景" && field != "清空" {
			return reply("可修改字段：名称、描述、性格、场景。"), nil
		}
		return update(func(c *state.Config) error {
			switch field {
			case "名称":
				c.Character.Name = value
			case "描述":
				c.Character.Description = value
			case "性格":
				c.Character.Personality = value
			case "场景":
				c.Character.Scenario = value
			case "清空":
				c.Character = state.Character{}
			}
			return nil
		})
	case "/群规则":
		if args == "" {
			return reply("本会话规则：\n" + shown(snap.Config.Note)), nil
		}
		action, value := splitCommand(args)
		if action != "清空" && (value == "" || (action != "设置" && action != "添加")) {
			return reply("用法：/群规则 设置|添加 内容，或 /群规则 清空"), nil
		}
		return update(func(c *state.Config) error {
			switch action {
			case "设置":
				c.Note = value
			case "添加":
				c.Note = appendLine(c.Note, value)
			case "清空":
				c.Note = ""
			}
			return nil
		})
	case "/世界书":
		return s.worldbook(ctx, e, args, snap)
	case "/群记忆":
		if e.Scope.Kind != "group" {
			return reply("请在需要查看的群内使用 /群记忆。"), nil
		}
		page := 1
		if args != "" {
			var err error
			page, err = strconv.Atoi(args)
			if err != nil || page < 1 {
				return reply("用法：/群记忆 [页码]"), nil
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
		entries := append([]state.Entry(nil), snap.Config.Entries[start:end]...)
		for i := range entries {
			entries[i].Content = cut(entries[i].Content, 300)
		}
		return reply(fmt.Sprintf("本群规则：\n%s\n\n本群世界书 第 %d/%d 页：\n%s\n\n完整内容：/世界书 查看 编号；完整规则：/群规则", shown(cut(snap.Config.Note, 1800)), page, pages, listEntries(entries, true))), nil
	case "/记忆":
		if args == "" {
			return reply("你在当前会话的个人记忆：\n" + shown(snap.Personal)), nil
		}
		action, value := splitCommand(args)
		if action == "清空" {
			value = ""
		} else if action != "设置" || value == "" {
			return reply("用法：/记忆 设置 内容，或 /记忆 清空。只能修改自己的个人记忆。"), nil
		}
		_, err := s.store.UpdatePersonal(ctx, e, snap.PersonalVersion, value)
		return reply("已保存你的个人记忆，下一轮对话生效。"), err
	case "/摘要":
		if args == "" {
			return reply(fmt.Sprintf("聊天摘要（版本 %d，截止轮次 %d）：\n%s", snap.Summary.Version, snap.Summary.Checkpoint, shown(snap.Summary.Text))), nil
		}
		if args == "更新" {
			return Result{Handled: true, RequestSummary: true}, nil
		}
		action, value := splitCommand(args)
		if action == "清空" {
			value = ""
		} else if action != "设置" || value == "" {
			return reply("用法：/摘要 更新、/摘要 设置 内容、/摘要 清空"), nil
		}
		err := s.store.SetSummary(ctx, e, snap.Summary.Version, value)
		return reply("已更新当前会话的摘要。"), err
	case "/修改记录":
		revisions, err := s.store.Revisions(ctx, e, 10)
		if err != nil {
			return Result{}, err
		}
		lines := []string{"最近修改（群公共设置及你自己的个人记忆）："}
		for _, r := range revisions {
			label := map[string]string{"config": "公共设置", "personal": "我的个人记忆", "summary": "摘要"}[r.Target]
			lines = append(lines, fmt.Sprintf("#%d %s · %s（成员 %s）· v%d · %s", r.ID, label, cut(r.ActorName, 30), domain.Key(e.Scope.Key(), r.ActorID)[:16], r.Version, r.CreatedAt.UTC().Format("01-02 15:04 UTC")))
		}
		if len(revisions) == 0 {
			lines = append(lines, "（空）")
		}
		lines = append(lines, "撤销仅支持对应设置的最新版本，避免覆盖之后的修改。")
		return reply(strings.Join(lines, "\n")), nil
	case "/撤销修改":
		id, err := strconv.ParseInt(args, 10, 64)
		if err != nil || id <= 0 {
			return reply("用法：/撤销修改 编号"), nil
		}
		err = s.store.Undo(ctx, e, id)
		return reply("已撤销该修改，下一轮生效。"), err
	case "/记忆状态":
		lines := []string{fmt.Sprintf("已保存世界书 %d 条；预算 %d 字符；个人记忆 %d 字符。", len(snap.Config.Entries), snap.Config.Budget, len([]rune(snap.Personal))), "以下是你上一轮普通对话的世界书注入情况："}
		for _, a := range snap.LastActivation {
			lines = append(lines, fmt.Sprintf("#%d %s：%s", a.ID, cut(a.Title, 30), a.Reason))
		}
		if len(snap.LastActivation) == 0 {
			lines = append(lines, "暂无注入记录；保存成功后将在下一轮普通对话判断。")
		}
		return reply(strings.Join(lines, "\n")), nil
	case "/重置上下文":
		err := s.store.ResetContext(ctx, e)
		return reply("已清除当前角色的聊天历史和摘要。角色、群规则、世界书、个人记忆和状态已保留。"), err
	case "/记到群":
		if e.Scope.Kind != "group" {
			return reply("请在目标群内使用 /记到群，私聊不能修改群记忆。"), nil
		}
		if args == "" {
			return reply("用法：/记到群 要保存的内容"), nil
		}
		return s.remember(ctx, e, args, snap, false)
	case "/记住":
		if args == "" {
			return reply("用法：/记住 要保存的内容"), nil
		}
		return s.remember(ctx, e, args, snap, isPersonal(args))
	default:
		if explicit {
			return s.remember(ctx, e, natural, snap, isPersonal(natural))
		}
	}
	return Result{}, nil
}
func appendLine(previous, text string) string {
	if previous == "" {
		return text
	}
	return previous + "\n" + text
}
func isPersonal(text string) bool {
	return strings.Contains(text, "我的偏好") || strings.Contains(text, "回复我时") || strings.Contains(text, "回答我时") || strings.HasPrefix(text, "我喜欢") || strings.HasPrefix(text, "我不喜欢") || strings.HasPrefix(text, "我叫")
}
func explicitMemory(text string) (string, bool) {
	if text == "记住了吗" || text == "记住了吗？" || text == "记住了吗?" || text == "记住了么" || text == "记住了" {
		return "", false
	}
	for _, prefix := range []string{"请帮我记住", "帮我记住", "请记住", "记住", "记一下"} {
		if strings.HasPrefix(text, prefix) {
			rest := strings.TrimLeft(strings.TrimPrefix(text, prefix), " ，,:：。\n\t")
			return rest, rest != ""
		}
	}
	if strings.HasPrefix(text, "以后每次回复") || strings.HasPrefix(text, "以后每轮回复") {
		return text, true
	}
	return "", false
}
func isRule(text string) bool {
	for _, phrase := range []string{"每次回复", "每轮回复", "每次回答", "每轮回答", "每次对话", "每轮对话", "回答末尾", "回复末尾"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}
func (s *Service) remember(ctx context.Context, e domain.Event, text string, snap state.Snapshot, personal bool) (Result, error) {
	if personal || (e.Scope.Kind == "private" && !isRule(text)) {
		_, err := s.store.UpdatePersonal(ctx, e, snap.PersonalVersion, appendLine(snap.Personal, text))
		return reply("已保存到你在当前会话的个人记忆。"), err
	}
	rule := isRule(text)
	_, err := s.store.UpdateConfig(ctx, e, snap.Config.Version, func(c *state.Config) error {
		if rule {
			c.Note = appendLine(c.Note, text)
		} else {
			id := 1
			for _, entry := range c.Entries {
				if entry.ID >= id {
					id = entry.ID + 1
				}
			}
			c.Entries = append(c.Entries, state.Entry{ID: id, Title: cut(text, 30), Content: text, Constant: true, Enabled: true, Priority: 50})
		}
		return nil
	})
	if rule {
		return reply("已保存为当前会话的公共回复规则，下一轮起对所有成员生效。"), err
	}
	return reply("已保存到当前会话的公共世界书；是否注入受启用状态和字符预算影响。"), err
}
