package state

import (
	"context"
	"encoding/json"
	"time"

	"github.com/supertx/qq-tavern/internal/domain"
)

// HistorySince returns oldest first so a summary checkpoint never skips an omitted turn.
func (s *Store) HistorySince(ctx context.Context, scope domain.Scope, checkpoint int64, limit int) ([]Turn, error) {
	if err := s.checkScope(scope); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,event_data,model,payload,created FROM tavern_turn WHERE scope=? AND id>? ORDER BY id LIMIT ?`, s.key(scope), checkpoint, limit)
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
	return out, rows.Err()
}

// ResetContext intentionally leaves character, rules, worldbook and personal memory intact.
func (s *Store) ResetContext(ctx context.Context, e domain.Event) error {
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
	if _, err = tx.ExecContext(ctx, `DELETE FROM tavern_turn WHERE scope=?`, s.key(e.Scope)); err != nil {
		return err
	}
	current, err := readSummary(ctx, tx, s.key(e.Scope))
	if err != nil {
		return err
	}
	if err = writeSummary(ctx, tx, s.key(e.Scope), Summary{Version: current.Version + 1}); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO tavern_operation(scope,event) VALUES(?,?)`, s.key(e.Scope), e.ID); err != nil {
		return err
	}
	return tx.Commit()
}
