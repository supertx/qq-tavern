package worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/state"
)

const importHelp = `批量导入标准 JSON：
私聊直接发送 .json 文件；群内引用文件并 @机器人发送 /导入。
预览后：/导入 确认 编号；取消：/导入 取消 编号。
预览 10 分钟有效，只能由原发起者在原会话确认。
需要对应的角色编辑、世界与公共设定权限；文件最多 8 MiB。
如果 QQ 没有投递文件或引用附件，请改用管理 API。`

func (s *Service) handleImport(ctx context.Context, e domain.Event) (bool, error) {
	parts := strings.Fields(e.Text)
	explicit := len(parts) > 0 && parts[0] == "/导入"
	if !explicit && !(e.Scope.Kind == "private" && len(e.Files) > 0) {
		return false, nil
	}
	answer := func(text string) (bool, error) { return true, s.reply(ctx, e, "import", text) }
	if explicit && len(parts) > 1 {
		if len(parts) != 3 || (parts[1] != "确认" && parts[1] != "取消") {
			return answer(importHelp)
		}
		if parts[1] == "取消" {
			if err := s.store.CancelImport(ctx, e.Scope, e.UserID, "qq", parts[2]); err != nil {
				return answer("取消失败：" + err.Error())
			}
			return answer("已取消导入。")
		}
		result, err := s.store.CommitImport(ctx, e.Scope, e.UserID, "qq", parts[2])
		if err != nil {
			return answer("导入未保存：" + err.Error())
		}
		return answer(fmt.Sprintf("导入已保存，共 %d 个角色；当前角色未切换。", len(result.Changes)))
	}
	files := append([]domain.FileAttachment{}, e.Files...)
	if explicit {
		for _, q := range e.Quotes {
			files = append(files, q.Files...)
		}
	}
	unique := []domain.FileAttachment{}
	seen := map[string]bool{}
	for _, f := range files {
		if !seen[f.URL] {
			seen[f.URL] = true
			unique = append(unique, f)
		}
	}
	if len(unique) == 0 {
		return answer(importHelp)
	}
	if len(unique) != 1 {
		return answer("每次请选择一个 JSON 文件；一个文件可以包含多个角色。")
	}
	policy, err := s.store.Policy(ctx)
	if err != nil {
		return true, err
	}
	if !policy.Groups[state.RoleEdit] && !policy.Groups[state.WorldEdit] {
		return answer("导入未保存：角色编辑和世界与公共设定均未开放，请由部署者通过管理接口开放。")
	}
	if unique[0].Size > state.MaxImportBytes {
		return answer("文件超过 8 MiB，请拆分后重试。")
	}
	fetcher, ok := s.sender.(interface {
		FetchDocument(context.Context, domain.Event, string) ([]byte, error)
	})
	if !ok {
		return answer("当前网关不支持文件下载，请使用管理 API。")
	}
	data, err := fetcher.FetchDocument(ctx, e, unique[0].URL)
	if err != nil {
		return answer("导入未保存：" + err.Error())
	}
	preview, err := s.store.PreviewImport(ctx, e.Scope, e.UserID, "qq", data)
	if err != nil {
		return answer("导入未保存：" + err.Error())
	}
	lines := []string{"导入预览（尚未保存）："}
	for i, c := range preview.Changes {
		if i >= 12 {
			lines = append(lines, fmt.Sprintf("另有 %d 个角色；完整清单可通过管理 API 预览。", len(preview.Changes)-i))
			break
		}
		action := "更新"
		if c.Action == "create" {
			action = "新增"
		}
		lines = append(lines, fmt.Sprintf("%s「%s」：%s；保存后世界书 %d 条", action, c.Name, strings.Join(c.Fields, "、"), c.WorldbookEntries))
	}
	lines = append(lines, "提供世界书条目数组会整体替换该角色的世界书；历史、个人记忆和状态值保留。",
		"10 分钟内确认：/导入 确认 "+preview.ID, "取消：/导入 取消 "+preview.ID)
	return answer(strings.Join(lines, "\n"))
}
