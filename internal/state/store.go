package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/supertx/qq-tavern/internal/domain"
	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	role *Role
}

func Open(path string) (*Store, error) {
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version > 2 {
		db.Close()
		return nil, fmt.Errorf("database schema version %d is newer than supported version 2", version)
	}
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS tavern_config(scope TEXT PRIMARY KEY, bot TEXT NOT NULL, kind TEXT NOT NULL, conversation TEXT NOT NULL, data TEXT NOT NULL, version INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS tavern_personal(scope TEXT NOT NULL, member TEXT NOT NULL, content TEXT NOT NULL, version INTEGER NOT NULL, PRIMARY KEY(scope,member));
CREATE TABLE IF NOT EXISTS tavern_summary(scope TEXT PRIMARY KEY, content TEXT NOT NULL, checkpoint INTEGER NOT NULL, version INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS tavern_revision(id INTEGER PRIMARY KEY AUTOINCREMENT, scope TEXT NOT NULL, target TEXT NOT NULL, member TEXT NOT NULL, actor TEXT NOT NULL, actor_name TEXT NOT NULL, event TEXT NOT NULL, before_data TEXT NOT NULL, after_data TEXT NOT NULL, version INTEGER NOT NULL, created INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS tavern_revision_scope ON tavern_revision(scope,id);
CREATE TABLE IF NOT EXISTS tavern_operation(scope TEXT NOT NULL, event TEXT NOT NULL, PRIMARY KEY(scope,event));
CREATE TABLE IF NOT EXISTS tavern_turn(id INTEGER PRIMARY KEY AUTOINCREMENT, scope TEXT NOT NULL, bot TEXT NOT NULL, kind TEXT NOT NULL, conversation TEXT NOT NULL, event_id TEXT NOT NULL, message_id TEXT NOT NULL, event_data TEXT NOT NULL, model TEXT NOT NULL, payload TEXT NOT NULL, completed INTEGER NOT NULL, created INTEGER NOT NULL, UNIQUE(scope,event_id), UNIQUE(scope,message_id));
CREATE INDEX IF NOT EXISTS tavern_turn_scope ON tavern_turn(scope,id);
CREATE TABLE IF NOT EXISTS tavern_activation(scope TEXT NOT NULL, member TEXT NOT NULL, data TEXT NOT NULL, PRIMARY KEY(scope,member));
`)
	if err != nil {
		db.Close()
		return nil, err
	}
	if err = migrateRoles(db, version); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) DB() *sql.DB  { return s.db }

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validScope(scope domain.Scope, member string) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if member == "" || (scope.Kind == "private" && scope.ID != member) {
		return errors.New("member does not belong to private conversation")
	}
	return nil
}
func validEvent(e domain.Event) error {
	if e.ID == "" {
		return errors.New("source event is required")
	}
	return validScope(e.Scope, e.UserID)
}
func readConfig(ctx context.Context, q queryer, key string) (Config, error) {
	c := Config{Budget: 6000, Entries: []Entry{}}
	var data string
	err := q.QueryRowContext(ctx, `SELECT data,version FROM tavern_config WHERE scope=?`, key).Scan(&data, &c.Version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	if err == nil {
		version := c.Version
		if err = json.Unmarshal([]byte(data), &c); err != nil {
			return c, err
		}
		c.Version = version
	}
	if c.ProfileName == "" {
		c.ProfileName = c.Character.Name
		err = q.QueryRowContext(ctx, `SELECT name FROM tavern_role WHERE storage_key=?`, key).Scan(&c.ProfileName)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return c, err
		}
		if c.ProfileName == "" {
			c.ProfileName = "默认角色"
		}
	}
	return c, nil
}
func readPersonal(ctx context.Context, q queryer, key string, member string) (string, int64, error) {
	var content string
	var version int64
	err := q.QueryRowContext(ctx, `SELECT content,version FROM tavern_personal WHERE scope=? AND member=?`, key, member).Scan(&content, &version)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return content, version, err
}
func readSummary(ctx context.Context, q queryer, key string) (Summary, error) {
	var out Summary
	err := q.QueryRowContext(ctx, `SELECT content,checkpoint,version FROM tavern_summary WHERE scope=?`, key).Scan(&out.Text, &out.Checkpoint, &out.Version)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return out, err
}
func (s *Store) Snapshot(ctx context.Context, scope domain.Scope, member string) (Snapshot, error) {
	var out Snapshot
	if err := s.validScope(scope, member); err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if out.Config, err = readConfig(ctx, tx, s.key(scope)); err != nil {
		return out, err
	}
	if out.Personal, out.PersonalVersion, err = readPersonal(ctx, tx, s.key(scope), member); err != nil {
		return out, err
	}
	if out.Summary, err = readSummary(ctx, tx, s.key(scope)); err != nil {
		return out, err
	}
	var data string
	err = tx.QueryRowContext(ctx, `SELECT data FROM tavern_activation WHERE scope=? AND member=?`, s.key(scope), member).Scan(&data)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if data != "" {
		if err = json.Unmarshal([]byte(data), &out.LastActivation); err != nil {
			return out, err
		}
	}
	if out.Status, err = readStatus(ctx, tx, s.key(scope), member); err != nil {
		return out, err
	}
	out.Role = s.Role()
	return out, tx.Commit()
}
func (s *Store) RecordActivation(ctx context.Context, e domain.Event, items []Activation) error {
	if err := s.validEvent(e); err != nil {
		return err
	}
	data, err := json.Marshal(items)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO tavern_activation(scope,member,data) VALUES(?,?,?) ON CONFLICT(scope,member) DO UPDATE SET data=excluded.data`, s.key(e.Scope), e.UserID, string(data))
	return err
}
func (s *Store) AddTurn(ctx context.Context, e domain.Event, model string, payload json.RawMessage) error {
	return s.AddTurnWithStatus(ctx, e, model, payload, nil)
}
func (s *Store) AddTurnWithStatus(ctx context.Context, e domain.Event, model string, payload json.RawMessage, pending *PendingStatus) error {
	if err := s.validEvent(e); err != nil {
		return err
	}
	if !json.Valid(payload) {
		return errors.New("turn payload must be valid JSON")
	}
	if e.MessageID == "" {
		return errors.New("source message ID is required")
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previousID int64
	var completed bool
	err = tx.QueryRowContext(ctx, `SELECT id,completed FROM tavern_turn WHERE scope=? AND message_id=?`, s.key(e.Scope), e.MessageID).Scan(&previousID, &completed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if completed || e.ObserveOnly {
			return nil
		}
		// A replied event replaces its earlier observation with a new checkpoint ID.
		if _, err = tx.ExecContext(ctx, `DELETE FROM tavern_turn WHERE id=?`, previousID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tavern_turn(scope,bot,kind,conversation,event_id,message_id,event_data,model,payload,completed,created) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, s.key(e.Scope), e.Scope.BotID, e.Scope.Kind, e.Scope.ID, e.ID, e.MessageID, string(data), model, string(payload), !e.ObserveOnly, time.Now().Unix())
	if err != nil {
		return err
	}
	if pending != nil {
		if err = s.commitStatus(ctx, tx, e, pending); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) History(ctx context.Context, scope domain.Scope, limit int) ([]Turn, error) {
	if err := s.checkScope(scope); err != nil {
		return nil, err
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,event_data,model,payload,created FROM tavern_turn WHERE scope=? ORDER BY id DESC LIMIT ?`, s.key(scope), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Turn{}
	for rows.Next() {
		var t Turn
		var event, payload string
		var created int64
		if err = rows.Scan(&t.ID, &event, &t.Model, &payload, &created); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(event), &t.Event); err != nil {
			return nil, err
		}
		t.Payload = json.RawMessage(payload)
		t.CreatedAt = time.Unix(created, 0)
		out = append(out, t)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
func (s *Store) Search(ctx context.Context, scope domain.Scope, query string, limit int) ([]Turn, error) {
	// Search is confined to the caller's trusted conversation; complete turns preserve tool pairs.
	turns, err := s.History(ctx, scope, 100)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 20 {
		limit = 10
	}
	out := []Turn{}
	query = strings.ToLower(query)
	for i := len(turns) - 1; i >= 0 && len(out) < limit; i-- {
		t := turns[i]
		if strings.Contains(strings.ToLower(t.Event.Text+string(t.Payload)), query) {
			out = append(out, t)
		}
	}
	return out, nil
}
func (s *Store) DueSummaries(ctx context.Context, now time.Time) ([]domain.Scope, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.bot,t.kind,t.conversation FROM tavern_turn t LEFT JOIN tavern_summary s ON s.scope=t.scope WHERE t.id>COALESCE(s.checkpoint,0) GROUP BY t.scope HAVING sum(t.completed)>=3 OR max(t.created)<=?`, now.Add(-2*time.Minute).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Scope{}
	for rows.Next() {
		var scope domain.Scope
		if err = rows.Scan(&scope.BotID, &scope.Kind, &scope.ID); err != nil {
			return nil, err
		}
		out = append(out, scope)
	}
	return out, rows.Err()
}
func (s *Store) Summary(ctx context.Context, scope domain.Scope) (Summary, error) {
	if err := s.checkScope(scope); err != nil {
		return Summary{}, err
	}
	return readSummary(ctx, s.db, s.key(scope))
}
func (s *Store) SaveSummary(ctx context.Context, scope domain.Scope, expectedVersion, checkpoint int64, text string) error {
	if err := s.checkScope(scope); err != nil {
		return err
	}
	if len([]rune(text)) > 16000 {
		return errors.New("summary exceeds 16000 characters")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := readSummary(ctx, tx, s.key(scope))
	if err != nil {
		return err
	}
	if current.Version != expectedVersion || checkpoint < current.Checkpoint {
		return ErrConflict
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM tavern_turn WHERE scope=? AND id=?`, s.key(scope), checkpoint).Scan(&exists); err != nil {
		return err
	}
	if exists != 1 {
		return fmt.Errorf("invalid summary checkpoint")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tavern_summary(scope,content,checkpoint,version) VALUES(?,?,?,?) ON CONFLICT(scope) DO UPDATE SET content=excluded.content,checkpoint=excluded.checkpoint,version=excluded.version`, s.key(scope), text, checkpoint, current.Version+1)
	if err != nil {
		return err
	}
	return tx.Commit()
}
