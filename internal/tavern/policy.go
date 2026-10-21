package tavern

import (
	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/state"
)

func commandGroups(e domain.Event, command, args, natural string, explicit bool) []state.CommandGroup {
	action, _ := splitCommand(args)
	switch command {
	case "/角色":
		if action == "切换" {
			return []state.CommandGroup{state.RoleSwitch}
		}
		if args != "" && action != "列表" {
			return []state.CommandGroup{state.RoleEdit}
		}
	case "/世界书":
		if args != "" && action != "列表" && action != "查看" {
			return []state.CommandGroup{state.WorldEdit}
		}
	case "/群规则":
		if args != "" {
			return []state.CommandGroup{state.WorldEdit}
		}
	case "/记忆":
		if args != "" {
			return []state.CommandGroup{state.PersonalMemory}
		}
	case "/状态设置", "/状态重置":
		return []state.CommandGroup{state.StatusManage}
	case "/状态项":
		if args != "" {
			return []state.CommandGroup{state.StatusManage}
		}
	case "/摘要":
		if args != "" {
			return []state.CommandGroup{state.ContextManage}
		}
	case "/重置上下文":
		return []state.CommandGroup{state.ContextManage}
	case "/记到群":
		if e.Scope.Kind == "group" && args != "" {
			return []state.CommandGroup{state.WorldEdit}
		}
	case "/记住":
		if args != "" {
			natural, explicit = args, true
		}
	}
	if explicit {
		if isPersonal(natural) || (e.Scope.Kind == "private" && !isRule(natural)) {
			return []state.CommandGroup{state.PersonalMemory}
		}
		return []state.CommandGroup{state.WorldEdit}
	}
	// Undo resolves required groups from the stored revision inside its transaction.
	return nil
}
