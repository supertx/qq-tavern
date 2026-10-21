package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/supertx/qq-tavern/internal/domain"
)

func validateConfig(c Config) error {
	if c.Budget < 0 || c.Budget > 24000 {
		return errors.New("worldbook budget must be 0–24000 characters")
	}
	if len(c.Entries) > 200 {
		return errors.New("at most 200 worldbook entries")
	}
	if len([]rune(c.Character.Name)) > 100 || len([]rune(c.Character.Description+c.Character.Personality+c.Character.Scenario)) > 12000 || len([]rune(c.Note)) > 6000 {
		return errors.New("character or group rules exceed their character limit")
	}
	ids := map[int]bool{}
	for _, e := range c.Entries {
		if e.ID <= 0 || ids[e.ID] {
			return errors.New("worldbook entry IDs must be positive and unique")
		}
		ids[e.ID] = true
		if strings.TrimSpace(e.Content) == "" || len([]rune(e.Content)) > 6000 || len([]rune(e.Title)) > 100 || e.Priority < 0 || e.Priority > 100 {
			return errors.New("invalid worldbook content, title or priority (0–100)")
		}
		if !e.Constant && len(e.Keywords) == 0 {
			return errors.New("keyword entries require at least one keyword")
		}
		if len(e.Keywords) > 20 {
			return errors.New("at most 20 keywords per entry")
		}
		for _, keyword := range e.Keywords {
			if strings.TrimSpace(keyword) == "" || len([]rune(keyword)) > 100 {
				return errors.New("invalid worldbook keyword")
			}
		}
	}
	return nil
}
func (s *Store) operationExists(ctx context.Context, tx *sql.Tx, e domain.Event) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tavern_operation WHERE scope=? AND event=?`, s.key(e.Scope), e.ID).Scan(&n)
	return n > 0, err
}
func (s *Store) recordMutation(ctx context.Context, tx *sql.Tx, e domain.Event, target, member string, before, after any, version int64) error {
	b, err := json.Marshal(before)
	if err != nil {
		return err
	}
	a, err := json.Marshal(after)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tavern_revision(scope,target,member,actor,actor_name,event,before_data,after_data,version,created) VALUES(?,?,?,?,?,?,?,?,?,?)`, s.key(e.Scope), target, member, e.UserID, e.UserName, e.ID, string(b), string(a), version, time.Now().Unix())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO tavern_operation(scope,event) VALUES(?,?)`, s.key(e.Scope), e.ID)
	return err
}
func writeConfig(ctx context.Context, tx *sql.Tx, scope domain.Scope, key string, c Config) error {
	if c.ProfileName == "" {
		c.ProfileName = strings.TrimSpace(c.Character.Name)
	}
	if c.ProfileName == "" {
		c.ProfileName = "默认角色"
	}
	if err := renameRole(ctx, tx, key, c.ProfileName); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tavern_config(scope,bot,kind,conversation,data,version) VALUES(?,?,?,?,?,?) ON CONFLICT(scope) DO UPDATE SET data=excluded.data,version=excluded.version`, key, scope.BotID, scope.Kind, scope.ID, string(data), c.Version)
	return err
}
func (s *Store) UpdateConfig(ctx context.Context, e domain.Event, expectedVersion int64, change func(*Config) error) (Config, error) {
	if err := s.validEvent(e); err != nil {
		return Config{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Config{}, err
	}
	defer tx.Rollback()
	before, err := readConfig(ctx, tx, s.key(e.Scope))
	if err != nil {
		return before, err
	}
	duplicate, err := s.operationExists(ctx, tx, e)
	if err != nil {
		return before, err
	}
	if duplicate {
		return before, nil
	}
	if before.Version != expectedVersion {
		return before, ErrConflict
	}
	data, _ := json.Marshal(before)
	var after Config
	if err = json.Unmarshal(data, &after); err != nil {
		return before, err
	}
	if err = change(&after); err != nil {
		return before, err
	}
	after.ProfileName = before.ProfileName
	if after.Character.Name != before.Character.Name && strings.TrimSpace(after.Character.Name) != "" {
		after.ProfileName = strings.TrimSpace(after.Character.Name)
	}
	after.Version = before.Version + 1
	if err = authorizeCommand(ctx, tx, configGroups(before, after)...); err != nil {
		return before, err
	}
	if err = validateConfig(after); err != nil {
		return before, err
	}
	if err = writeConfig(ctx, tx, e.Scope, s.key(e.Scope), after); err != nil {
		return before, err
	}
	if err = s.recordMutation(ctx, tx, e, "config", "", before, after, after.Version); err != nil {
		return before, err
	}
	return after, tx.Commit()
}
func writePersonal(ctx context.Context, tx *sql.Tx, key string, member, content string, version int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO tavern_personal(scope,member,content,version) VALUES(?,?,?,?) ON CONFLICT(scope,member) DO UPDATE SET content=excluded.content,version=excluded.version`, key, member, content, version)
	return err
}
func (s *Store) UpdatePersonal(ctx context.Context, e domain.Event, expectedVersion int64, text string) (int64, error) {
	if err := s.validEvent(e); err != nil {
		return 0, err
	}
	if len([]rune(text)) > 6000 {
		return 0, errors.New("personal memory exceeds 6000 characters")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = authorizeCommand(ctx, tx, PersonalMemory); err != nil {
		return 0, err
	}
	before, version, err := readPersonal(ctx, tx, s.key(e.Scope), e.UserID)
	if err != nil {
		return 0, err
	}
	duplicate, err := s.operationExists(ctx, tx, e)
	if err != nil {
		return 0, err
	}
	if duplicate {
		return version, nil
	}
	if version != expectedVersion {
		return version, ErrConflict
	}
	version++
	if err = writePersonal(ctx, tx, s.key(e.Scope), e.UserID, text, version); err != nil {
		return 0, err
	}
	if err = s.recordMutation(ctx, tx, e, "personal", e.UserID, before, text, version); err != nil {
		return 0, err
	}
	return version, tx.Commit()
}
func (s *Store) Revisions(ctx context.Context, e domain.Event, limit int) ([]Revision, error) {
	if err := s.validEvent(e); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 50 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,target,actor,actor_name,event,before_data,after_data,version,created FROM tavern_revision WHERE scope=? AND (member='' OR member=?) ORDER BY id DESC LIMIT ?`, s.key(e.Scope), e.UserID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		var r Revision
		var before, after string
		var created int64
		if err = rows.Scan(&r.ID, &r.Target, &r.ActorID, &r.ActorName, &r.EventID, &before, &after, &r.Version, &created); err != nil {
			return nil, err
		}
		r.Before = json.RawMessage(before)
		r.After = json.RawMessage(after)
		r.CreatedAt = time.Unix(created, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) Undo(ctx context.Context, e domain.Event, revisionID int64) error {
	if err := s.validEvent(e); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	duplicate, err := s.operationExists(ctx, tx, e)
	if err != nil {
		return err
	}
	if duplicate {
		return nil
	}
	var target, member, data string
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT target,member,before_data,version FROM tavern_revision WHERE id=? AND scope=? AND (member='' OR member=?)`, revisionID, s.key(e.Scope), e.UserID).Scan(&target, &member, &data, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var before, after any
	switch target {
	case "config":
		current, err := readConfig(ctx, tx, s.key(e.Scope))
		if err != nil {
			return err
		}
		if current.Version != version {
			return ErrConflict
		}
		var restored Config
		if err = json.Unmarshal([]byte(data), &restored); err != nil {
			return err
		}
		restored.Version = current.Version + 1
		version = restored.Version
		if err = authorizeCommand(ctx, tx, configGroups(current, restored)...); err != nil {
			return err
		}
		if err = writeConfig(ctx, tx, e.Scope, s.key(e.Scope), restored); err != nil {
			return err
		}
		before, after = current, restored
	case "personal":
		if err = authorizeCommand(ctx, tx, PersonalMemory); err != nil {
			return err
		}
		current, v, err := readPersonal(ctx, tx, s.key(e.Scope), e.UserID)
		if err != nil {
			return err
		}
		if v != version {
			return ErrConflict
		}
		var restored string
		if err = json.Unmarshal([]byte(data), &restored); err != nil {
			return err
		}
		version = v + 1
		if err = writePersonal(ctx, tx, s.key(e.Scope), e.UserID, restored, version); err != nil {
			return err
		}
		before, after = current, restored
	case "summary":
		if err = authorizeCommand(ctx, tx, ContextManage); err != nil {
			return err
		}
		current, err := readSummary(ctx, tx, s.key(e.Scope))
		if err != nil {
			return err
		}
		if current.Version != version {
			return ErrConflict
		}
		var restored Summary
		if err = json.Unmarshal([]byte(data), &restored); err != nil {
			return err
		}
		restored.Version = current.Version + 1
		version = restored.Version
		if err = writeSummary(ctx, tx, s.key(e.Scope), restored); err != nil {
			return err
		}
		before, after = current, restored
	case "status_shared", "status_member", "status_schema":
		if err = authorizeCommand(ctx, tx, StatusManage); err != nil {
			return err
		}
		return s.undoStatus(ctx, tx, e, target, member, data, version)
	default:
		return fmt.Errorf("unknown revision type %q", target)
	}
	if err = s.recordMutation(ctx, tx, e, target, member, before, after, version); err != nil {
		return err
	}
	return tx.Commit()
}
func writeSummary(ctx context.Context, tx *sql.Tx, key string, value Summary) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO tavern_summary(scope,content,checkpoint,version) VALUES(?,?,?,?) ON CONFLICT(scope) DO UPDATE SET content=excluded.content,checkpoint=excluded.checkpoint,version=excluded.version`, key, value.Text, value.Checkpoint, value.Version)
	return err
}
func (s *Store) SetSummary(ctx context.Context, e domain.Event, expectedVersion int64, text string) error {
	if err := s.validEvent(e); err != nil {
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
	if err = authorizeCommand(ctx, tx, ContextManage); err != nil {
		return err
	}
	before, err := readSummary(ctx, tx, s.key(e.Scope))
	if err != nil {
		return err
	}
	duplicate, err := s.operationExists(ctx, tx, e)
	if err != nil {
		return err
	}
	if duplicate {
		return nil
	}
	if before.Version != expectedVersion {
		return ErrConflict
	}
	var last int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(id),0) FROM tavern_turn WHERE scope=?`, s.key(e.Scope)).Scan(&last); err != nil {
		return err
	}
	after := Summary{Text: text, Checkpoint: last, Version: before.Version + 1}
	if err = writeSummary(ctx, tx, s.key(e.Scope), after); err != nil {
		return err
	}
	if err = s.recordMutation(ctx, tx, e, "summary", "", before, after, after.Version); err != nil {
		return err
	}
	return tx.Commit()
}
