package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
)

type CommandGroup string

const (
	RoleEdit       CommandGroup = "role_edit"
	RoleSwitch     CommandGroup = "role_switch"
	WorldEdit      CommandGroup = "world_edit"
	PersonalMemory CommandGroup = "personal_memory"
	StatusManage   CommandGroup = "status_manage"
	ContextManage  CommandGroup = "context_manage"
)

type GroupDescription struct {
	ID          CommandGroup `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
}

func CommandGroups() []GroupDescription {
	return []GroupDescription{
		{RoleEdit, "角色编辑", "创建角色、修改或清空角色人设"},
		{RoleSwitch, "角色切换", "切换当前群或私聊使用的角色"},
		{WorldEdit, "世界与公共设定", "编辑世界书、注入预算、群规则和公共记忆"},
		{PersonalMemory, "个人记忆", "设置、追加或清空自己的个人记忆"},
		{StatusManage, "状态管理", "编辑状态项定义，手动修改或重置状态"},
		{ContextManage, "上下文管理", "手动更新或清空摘要、重置聊天上下文"},
	}
}

type CommandPolicy struct {
	Groups  map[CommandGroup]bool `json:"groups"`
	Version int64                 `json:"version"`
}

var ErrForbidden = errors.New("命令组未开放")
var ErrExpiredImport = errors.New("import preview expired or cancelled")

// This marker is only installed by trusted Go entrypoints, never deserialized
// from a QQ event or an HTTP body. Internal/background writes are separate.
type qqCommandKey struct{}
type commandGroupsKey struct{}

func WithQQCommand(ctx context.Context) context.Context {
	return context.WithValue(ctx, qqCommandKey{}, true)
}

func WithCommandGroups(ctx context.Context, groups ...CommandGroup) context.Context {
	return context.WithValue(WithQQCommand(ctx), commandGroupsKey{}, groups)
}

func migrateAdmin(db *sql.DB, version int) error {
	if version >= 4 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
CREATE TABLE IF NOT EXISTS tavern_instance(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS tavern_command_policy(id INTEGER PRIMARY KEY CHECK(id=1),data TEXT NOT NULL,version INTEGER NOT NULL);
INSERT OR IGNORE INTO tavern_command_policy VALUES(1,'{}',0);
CREATE TABLE IF NOT EXISTS tavern_import(id TEXT PRIMARY KEY,scope TEXT NOT NULL,actor TEXT NOT NULL,source TEXT NOT NULL,payload TEXT NOT NULL,result TEXT NOT NULL DEFAULT '',status TEXT NOT NULL,created INTEGER NOT NULL,expires INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS tavern_import_expiry ON tavern_import(expires);
CREATE TABLE IF NOT EXISTS tavern_admin_audit(id INTEGER PRIMARY KEY AUTOINCREMENT,actor TEXT NOT NULL,action TEXT NOT NULL,scope TEXT NOT NULL,before_data TEXT NOT NULL,after_data TEXT NOT NULL,created INTEGER NOT NULL);
ALTER TABLE tavern_active_role ADD COLUMN version INTEGER NOT NULL DEFAULT 0;
PRAGMA user_version=4;`)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO tavern_instance(key,value) VALUES('id',?)`, uuid.NewString()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) InstanceID(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM tavern_instance WHERE key='id'`).Scan(&id)
	return id, err
}

func readPolicy(ctx context.Context, q queryer) (CommandPolicy, error) {
	p := CommandPolicy{Groups: map[CommandGroup]bool{}}
	for _, g := range CommandGroups() {
		p.Groups[g.ID] = false
	}
	var data string
	if err := q.QueryRowContext(ctx, `SELECT data,version FROM tavern_command_policy WHERE id=1`).Scan(&data, &p.Version); err != nil {
		return p, err
	}
	err := json.Unmarshal([]byte(data), &p.Groups)
	return p, err
}

func (s *Store) Policy(ctx context.Context) (CommandPolicy, error) {
	return readPolicy(ctx, s.db)
}

func (s *Store) SetPolicy(ctx context.Context, expected int64, groups map[CommandGroup]bool) (CommandPolicy, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CommandPolicy{}, err
	}
	defer tx.Rollback()
	before, err := readPolicy(ctx, tx)
	if err != nil {
		return before, err
	}
	if before.Version != expected {
		return before, ErrConflict
	}
	after := CommandPolicy{Groups: map[CommandGroup]bool{}, Version: before.Version + 1}
	for k, v := range before.Groups {
		after.Groups[k] = v
	}
	if len(groups) == 0 {
		return before, errors.New("groups must contain at least one command group")
	}
	for k, v := range groups {
		if _, ok := after.Groups[k]; !ok {
			return before, fmt.Errorf("unknown command group %q", k)
		}
		after.Groups[k] = v
	}
	data, _ := json.Marshal(after.Groups)
	if _, err = tx.ExecContext(ctx, `UPDATE tavern_command_policy SET data=?,version=? WHERE id=1`, string(data), after.Version); err != nil {
		return before, err
	}
	if err = adminAudit(ctx, tx, "command_policy", "", before, after); err != nil {
		return before, err
	}
	return after, tx.Commit()
}

func checkGroups(ctx context.Context, q queryer, groups ...CommandGroup) error {
	if len(groups) == 0 {
		return nil
	}
	p, err := readPolicy(ctx, q)
	if err != nil {
		return err
	}
	for _, group := range groups {
		if !p.Groups[group] {
			name := string(group)
			for _, g := range CommandGroups() {
				if g.ID == group {
					name = g.Name
				}
			}
			return fmt.Errorf("%w：%s；请由部署者通过管理接口开放", ErrForbidden, name)
		}
	}
	return nil
}

func (s *Store) CheckCommandGroups(ctx context.Context, groups ...CommandGroup) error {
	return checkGroups(ctx, s.db, groups...)
}

func authorizeCommand(ctx context.Context, q queryer, groups ...CommandGroup) error {
	if marked, _ := ctx.Value(qqCommandKey{}).(bool); !marked {
		return nil
	}
	required, _ := ctx.Value(commandGroupsKey{}).([]CommandGroup)
	all := append(append([]CommandGroup{}, required...), groups...)
	return checkGroups(ctx, q, all...)
}

func configGroups(before, after Config) []CommandGroup {
	var groups []CommandGroup
	if before.Character != after.Character || before.ProfileName != after.ProfileName {
		groups = append(groups, RoleEdit)
	}
	if before.Note != after.Note || before.Budget != after.Budget || !reflect.DeepEqual(before.Entries, after.Entries) {
		groups = append(groups, WorldEdit)
	}
	return groups
}

func adminAudit(ctx context.Context, tx *sql.Tx, action, scope string, before, after any) error {
	b, err := json.Marshal(before)
	if err != nil {
		return err
	}
	a, err := json.Marshal(after)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tavern_admin_audit(actor,action,scope,before_data,after_data,created) VALUES('admin-api',?,?,?,?,?)`, action, scope, string(b), string(a), time.Now().Unix())
	return err
}
