package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/supertx/qq-tavern/internal/domain"
)

// Role is an internal storage namespace. Scope remains the original QQ destination.
type Role struct {
	ID     int64
	Scope  domain.Scope
	Key    string
	Name   string
	Active bool
}

func migrateRoles(db *sql.DB, version int) error {
	if version >= 2 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
CREATE TABLE tavern_role(id INTEGER PRIMARY KEY AUTOINCREMENT, scope TEXT NOT NULL, storage_key TEXT NOT NULL UNIQUE, name TEXT NOT NULL, bot TEXT NOT NULL, kind TEXT NOT NULL, conversation TEXT NOT NULL, UNIQUE(scope,name));
CREATE TABLE tavern_active_role(scope TEXT PRIMARY KEY, role_id INTEGER NOT NULL REFERENCES tavern_role(id));
CREATE TABLE tavern_message_role(scope TEXT NOT NULL, message TEXT NOT NULL, role_id INTEGER NOT NULL REFERENCES tavern_role(id), PRIMARY KEY(scope,message));
CREATE TABLE tavern_status_schema(scope TEXT PRIMARY KEY, data TEXT NOT NULL, version INTEGER NOT NULL);
CREATE TABLE tavern_status(scope TEXT NOT NULL, member TEXT NOT NULL, data TEXT NOT NULL, version INTEGER NOT NULL, PRIMARY KEY(scope,member));
ALTER TABLE tavern_revision ADD COLUMN reason TEXT NOT NULL DEFAULT '';
INSERT INTO tavern_role(scope,storage_key,name,bot,kind,conversation)
 SELECT x.scope,x.scope,COALESCE(NULLIF(trim(json_extract(c.data,'$.character.name')),''),'默认角色'),x.bot,x.kind,x.conversation
 FROM (SELECT scope,bot,kind,conversation FROM tavern_config UNION SELECT scope,bot,kind,conversation FROM tavern_turn) x LEFT JOIN tavern_config c ON c.scope=x.scope;
INSERT INTO tavern_active_role SELECT scope,id FROM tavern_role;
INSERT OR IGNORE INTO tavern_message_role SELECT r.scope,t.message_id,r.id FROM tavern_turn t JOIN tavern_role r ON r.storage_key=t.scope;
PRAGMA user_version=2;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) key(scope domain.Scope) string {
	if s.role != nil {
		return s.role.Key
	}
	return scope.Key() // Legacy callers address the first role; production binds explicitly.
}
func (s *Store) validScope(scope domain.Scope, member string) error {
	if err := validScope(scope, member); err != nil {
		return err
	}
	return s.checkScope(scope)
}
func (s *Store) checkScope(scope domain.Scope) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if s.role != nil && s.role.Scope != scope {
		return errors.New("role belongs to another conversation")
	}
	return nil
}
func (s *Store) validEvent(e domain.Event) error {
	if err := validEvent(e); err != nil {
		return err
	}
	return s.checkScope(e.Scope)
}
func (s *Store) Role() *Role {
	if s.role == nil {
		return nil
	}
	r := *s.role
	return &r
}

func scanRole(row *sql.Row, scope domain.Scope) (Role, error) {
	r := Role{Scope: scope}
	err := row.Scan(&r.ID, &r.Key, &r.Name)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return r, err
}
func ensureActive(ctx context.Context, tx *sql.Tx, scope domain.Scope) (Role, error) {
	r, err := scanRole(tx.QueryRowContext(ctx, `SELECT r.id,r.storage_key,r.name FROM tavern_active_role a JOIN tavern_role r ON r.id=a.role_id WHERE a.scope=?`, scope.Key()), scope)
	if !errors.Is(err, ErrNotFound) {
		return r, err
	}
	c, err := readConfig(ctx, tx, scope.Key())
	if err != nil {
		return r, err
	}
	name := strings.TrimSpace(c.Character.Name)
	if name == "" {
		name = "默认角色"
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO tavern_role(scope,storage_key,name,bot,kind,conversation) VALUES(?,?,?,?,?,?)`, scope.Key(), scope.Key(), name, scope.BotID, scope.Kind, scope.ID)
	if err != nil {
		return r, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return r, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tavern_active_role(scope,role_id) VALUES(?,?)`, scope.Key(), id)
	return Role{ID: id, Scope: scope, Key: scope.Key(), Name: name, Active: true}, err
}

// BindActive captures the role once. The returned view shares the database pool.
func (s *Store) BindActive(ctx context.Context, scope domain.Scope) (*Store, error) {
	if err := s.checkScope(scope); err != nil {
		return nil, err
	}
	if s.role != nil {
		return s, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := ensureActive(ctx, tx, scope)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &Store{db: s.db, role: &r}, nil
}
func (s *Store) BindRole(ctx context.Context, scope domain.Scope, name string) (*Store, error) {
	if err := s.checkScope(scope); err != nil {
		return nil, err
	}
	r, err := scanRole(s.db.QueryRowContext(ctx, `SELECT id,storage_key,name FROM tavern_role WHERE scope=? AND name=?`, scope.Key(), strings.TrimSpace(name)), scope)
	if err != nil {
		return nil, err
	}
	return &Store{db: s.db, role: &r}, nil
}

// BindMessage keeps observations and later replies attached to their original role.
func (s *Store) BindMessage(ctx context.Context, e domain.Event) (*Store, error) {
	if err := s.validEvent(e); err != nil {
		return nil, err
	}
	if e.MessageID == "" {
		return nil, errors.New("source message ID is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := ensureActive(ctx, tx, e.Scope)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO tavern_message_role(scope,message,role_id) VALUES(?,?,?)`, e.Scope.Key(), e.MessageID, r.ID)
	if err != nil {
		return nil, err
	}
	r, err = scanRole(tx.QueryRowContext(ctx, `SELECT r.id,r.storage_key,r.name FROM tavern_message_role m JOIN tavern_role r ON r.id=m.role_id WHERE m.scope=? AND m.message=?`, e.Scope.Key(), e.MessageID), e.Scope)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &Store{db: s.db, role: &r}, nil
}
func (s *Store) Roles(ctx context.Context, scope domain.Scope) ([]Role, error) {
	if _, err := s.BindActive(ctx, scope); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.storage_key,r.name,r.id=a.role_id FROM tavern_role r JOIN tavern_active_role a ON a.scope=r.scope WHERE r.scope=? ORDER BY r.id`, scope.Key())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Role
	for rows.Next() {
		r := Role{Scope: scope}
		if err = rows.Scan(&r.ID, &r.Key, &r.Name, &r.Active); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) SelectRole(ctx context.Context, e domain.Event, name string, create bool) error {
	if err := s.validEvent(e); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 || strings.ContainsAny(name, "\r\n") {
		return errors.New("角色名须为1～100字的单行文本")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	group := RoleSwitch
	if create {
		group = RoleEdit
	}
	if err = authorizeCommand(ctx, tx, group); err != nil {
		return err
	}
	if _, err = ensureActive(ctx, tx, e.Scope); err != nil {
		return err
	}
	var duplicate int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM tavern_operation WHERE scope=? AND event=?`, e.Scope.Key(), e.ID).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate > 0 {
		return nil
	}
	r, err := scanRole(tx.QueryRowContext(ctx, `SELECT id,storage_key,name FROM tavern_role WHERE scope=? AND name=?`, e.Scope.Key(), name), e.Scope)
	if create {
		if err == nil {
			return errors.New("已有同名角色，请使用 /角色 切换")
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		key := domain.Key(e.Scope.Key(), uuid.NewString())
		res, err := tx.ExecContext(ctx, `INSERT INTO tavern_role(scope,storage_key,name,bot,kind,conversation) VALUES(?,?,?,?,?,?)`, e.Scope.Key(), key, name, e.Scope.BotID, e.Scope.Kind, e.Scope.ID)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		r = Role{ID: id, Key: key, Name: name, Scope: e.Scope}
		if err = writeConfig(ctx, tx, e.Scope, key, Config{Character: Character{Name: name}, Budget: 6000, Entries: []Entry{}, Version: 1}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	qqCommand, _ := ctx.Value(qqCommandKey{}).(bool)
	if !create || !qqCommand {
		if _, err = tx.ExecContext(ctx, `UPDATE tavern_active_role SET role_id=?,version=version+1 WHERE scope=?`, r.ID, e.Scope.Key()); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO tavern_operation(scope,event) VALUES(?,?)`, e.Scope.Key(), e.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func renameRole(ctx context.Context, tx *sql.Tx, key, name string) error {
	// Clearing the character prompt does not delete or rename its saved profile.
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if strings.ContainsAny(name, "\r\n") {
		return errors.New("角色名不能换行")
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tavern_role WHERE name=? AND storage_key<>? AND scope=(SELECT scope FROM tavern_role WHERE storage_key=?)`, name, key, key).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("本会话已有同名角色")
	}
	_, err := tx.ExecContext(ctx, `UPDATE tavern_role SET name=? WHERE storage_key=?`, name, key)
	return err
}

func (s *Store) DueRoleSummaries(ctx context.Context, now time.Time) ([]Role, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.storage_key,r.name,r.bot,r.kind,r.conversation FROM tavern_role r JOIN tavern_turn t ON t.scope=r.storage_key LEFT JOIN tavern_summary s ON s.scope=t.scope WHERE t.id>COALESCE(s.checkpoint,0) GROUP BY r.id HAVING sum(t.completed)>=3 OR max(t.created)<=?`, now.Add(-2*time.Minute).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Role
	for rows.Next() {
		var r Role
		if err = rows.Scan(&r.ID, &r.Key, &r.Name, &r.Scope.BotID, &r.Scope.Kind, &r.Scope.ID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) BindRoleID(ctx context.Context, scope domain.Scope, id int64) (*Store, error) {
	if err := s.checkScope(scope); err != nil {
		return nil, err
	}
	r, err := scanRole(s.db.QueryRowContext(ctx, `SELECT id,storage_key,name FROM tavern_role WHERE scope=? AND id=?`, scope.Key(), id), scope)
	if err != nil {
		return nil, fmt.Errorf("role: %w", err)
	}
	return &Store{db: s.db, role: &r}, nil
}
