package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/supertx/qq-tavern/internal/domain"
)

type ScopeInfo struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	ActiveRoleID int64  `json:"active_role_id"`
	ActiveRole   string `json:"active_role"`
	Version      int64  `json:"version"`
}

// Admin lookups always resolve through the configured bot, never a bot supplied
// in an HTTP body. No member memory or internal storage keys are returned.
func (s *Store) Scopes(ctx context.Context, bot string) ([]ScopeInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.scope,r.kind,r.id,r.name,a.version FROM tavern_active_role a JOIN tavern_role r ON r.id=a.role_id WHERE r.bot=? ORDER BY r.scope`, bot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScopeInfo{}
	for rows.Next() {
		var item ScopeInfo
		if err = rows.Scan(&item.ID, &item.Kind, &item.ActiveRoleID, &item.ActiveRole, &item.Version); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ResolveScope(ctx context.Context, bot, id string) (domain.Scope, error) {
	var scope domain.Scope
	err := s.db.QueryRowContext(ctx, `SELECT bot,kind,conversation FROM tavern_role WHERE bot=? AND scope=? LIMIT 1`, bot, id).Scan(&scope.BotID, &scope.Kind, &scope.ID)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return scope, err
}

func (s *Store) RoleConfig(ctx context.Context) (Config, error) {
	if s.role == nil {
		return Config{}, errors.New("a role must be selected")
	}
	return readConfig(ctx, s.db, s.role.Key)
}

func (s *Store) RoleSchema(ctx context.Context) (StatusSchema, error) {
	if s.role == nil {
		return StatusSchema{}, errors.New("a role must be selected")
	}
	return readStatusSchema(ctx, s.db, s.role.Key)
}

func validateRoleName(name string) error {
	if name == "" || name != strings.TrimSpace(name) || len([]rune(name)) > 100 || strings.ContainsAny(name, "\r\n") {
		return errors.New("角色名须为 1～100 字的单行文本，且不能包含首尾空格")
	}
	return nil
}

func createConfiguredRole(ctx context.Context, tx *sql.Tx, scope domain.Scope, c Config) (Role, error) {
	if err := validateRoleName(c.ProfileName); err != nil {
		return Role{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tavern_role WHERE scope=? AND name=?`, scope.Key(), c.ProfileName).Scan(&count); err != nil {
		return Role{}, err
	}
	if count != 0 {
		return Role{}, ErrConflict
	}
	r := Role{Scope: scope, Key: domain.Key(scope.Key(), uuid.NewString()), Name: c.ProfileName}
	result, err := tx.ExecContext(ctx, `INSERT INTO tavern_role(scope,storage_key,name,bot,kind,conversation) VALUES(?,?,?,?,?,?)`, scope.Key(), r.Key, r.Name, scope.BotID, scope.Kind, scope.ID)
	if err != nil {
		return r, err
	}
	r.ID, err = result.LastInsertId()
	if err != nil {
		return r, err
	}
	c.Version = 1
	if err = validateConfig(c); err != nil {
		return r, err
	}
	return r, writeConfig(ctx, tx, scope, r.Key, c)
}

func adminRevision(ctx context.Context, tx *sql.Tx, r Role, target string, before, after any, version int64) error {
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	_, err := tx.ExecContext(ctx, `INSERT INTO tavern_revision(scope,target,member,actor,actor_name,event,before_data,after_data,version,created) VALUES(?,?,'','admin-api','管理接口',?,?,?,?,?)`,
		r.Key, target, "admin:"+uuid.NewString(), string(b), string(a), version, time.Now().Unix())
	if err != nil {
		return err
	}
	return adminAudit(ctx, tx, target, r.Scope.Key(), before, after)
}

func (s *Store) AdminCreateRole(ctx context.Context, scope domain.Scope, name string) (Role, error) {
	if err := s.checkScope(scope); err != nil {
		return Role{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Role{}, err
	}
	defer tx.Rollback()
	if _, err = ensureActive(ctx, tx, scope); err != nil {
		return Role{}, err
	}
	c := Config{ProfileName: name, Character: Character{Name: name}, Entries: []Entry{}, Budget: 6000, Version: 1}
	r, err := createConfiguredRole(ctx, tx, scope, c)
	if err != nil {
		return r, err
	}
	if err = adminAudit(ctx, tx, "create_role", scope.Key(), nil, map[string]any{"id": r.ID, "name": r.Name}); err != nil {
		return r, err
	}
	return r, tx.Commit()
}

func (s *Store) AdminSelectRole(ctx context.Context, scope domain.Scope, roleID, expectedVersion int64) error {
	if err := s.checkScope(scope); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := ensureActive(ctx, tx, scope)
	if err != nil {
		return err
	}
	var version int64
	if err = tx.QueryRowContext(ctx, `SELECT version FROM tavern_active_role WHERE scope=?`, scope.Key()).Scan(&version); err != nil {
		return err
	}
	if version != expectedVersion {
		return ErrConflict
	}
	next, err := scanRole(tx.QueryRowContext(ctx, `SELECT id,storage_key,name FROM tavern_role WHERE scope=? AND id=?`, scope.Key(), roleID), scope)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tavern_active_role SET role_id=?,version=version+1 WHERE scope=?`, next.ID, scope.Key()); err != nil {
		return err
	}
	if err = adminAudit(ctx, tx, "select_role", scope.Key(), current.ID, next.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AdminPatchConfig(ctx context.Context, expected int64, patch ConfigPatch) (Config, error) {
	if s.role == nil {
		return Config{}, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Config{}, err
	}
	defer tx.Rollback()
	before, err := readConfig(ctx, tx, s.role.Key)
	if err != nil {
		return before, err
	}
	if before.Version != expected {
		return before, ErrConflict
	}
	after, err := patch.Apply(before)
	if err != nil {
		return before, err
	}
	after.Version = before.Version + 1
	if err = writeConfig(ctx, tx, s.role.Scope, s.role.Key, after); err != nil {
		return before, err
	}
	if err = adminRevision(ctx, tx, *s.role, "config", before, after, after.Version); err != nil {
		return before, err
	}
	return after, tx.Commit()
}

func (s *Store) AdminSetSchema(ctx context.Context, expected int64, definitions []StatusDefinition) (StatusSchema, error) {
	if s.role == nil {
		return StatusSchema{}, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StatusSchema{}, err
	}
	defer tx.Rollback()
	before, err := readStatusSchema(ctx, tx, s.role.Key)
	if err != nil {
		return before, err
	}
	if before.Version != expected {
		return before, ErrConflict
	}
	after := StatusSchema{Definitions: definitions, Version: before.Version + 1}
	if err = s.checkSchemaValues(ctx, tx, s.role.Key, before, after); err != nil {
		return before, err
	}
	if err = writeStatusSchema(ctx, tx, s.role.Key, after); err != nil {
		return before, err
	}
	if err = adminRevision(ctx, tx, *s.role, "status_schema", before, after, after.Version); err != nil {
		return before, err
	}
	return after, tx.Commit()
}
