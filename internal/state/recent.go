package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/supertx/qq-tavern/internal/domain"
)

func migrateRecent(db *sql.DB, version int) error {
	if version >= 3 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS tavern_recent(scope TEXT NOT NULL,message TEXT NOT NULL,message_index TEXT NOT NULL,data TEXT NOT NULL,created INTEGER NOT NULL,expires INTEGER NOT NULL,PRIMARY KEY(scope,message));
 CREATE INDEX IF NOT EXISTS tavern_recent_index ON tavern_recent(scope,message_index);
 CREATE INDEX IF NOT EXISTS tavern_recent_created ON tavern_recent(created);
 CREATE INDEX IF NOT EXISTS tavern_recent_expiry ON tavern_recent(expires);
 PRAGMA user_version=3;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Recent messages belong to the role bound at reception, not the mutable active
// pointer. Attachment URLs remain internal and expire sooner than message text.
func (s *Store) RecordRecent(ctx context.Context, e domain.Event) error {
	if err := s.validEvent(e); err != nil {
		return err
	}
	if e.Scope.Kind != "group" {
		return nil
	}
	now := time.Now()
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	e.Text = clipRecent(e.Text, 4000)
	e.Quotes = nil // Only cache the original message attachments for index lookup.
	if len(e.Images) > 10 {
		e.Images = e.Images[:10]
	}
	if len(e.Files) > 10 {
		e.Files = e.Files[:10]
	}
	e.UserName = clipRecent(e.UserName, 100)
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM tavern_recent WHERE expires<=?`, now.Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tavern_recent SET data=json_set(data,'$.images',json('[]'),'$.files',json('[]')) WHERE created<? AND (json_array_length(data,'$.images')>0 OR json_array_length(data,'$.files')>0)`, now.Add(-time.Hour).UnixNano()); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tavern_recent(scope,message,message_index,data,created,expires) VALUES(?,?,?,?,?,?) ON CONFLICT(scope,message) DO UPDATE SET
 message_index=CASE WHEN tavern_recent.message_index='' THEN excluded.message_index ELSE tavern_recent.message_index END,
 data=json_set(tavern_recent.data,
 '$.images',CASE WHEN COALESCE(json_array_length(tavern_recent.data,'$.images'),0)=0 THEN COALESCE(json_extract(excluded.data,'$.images'),json('[]')) ELSE json_extract(tavern_recent.data,'$.images') END,
 '$.files',CASE WHEN COALESCE(json_array_length(tavern_recent.data,'$.files'),0)=0 THEN COALESCE(json_extract(excluded.data,'$.files'),json('[]')) ELSE json_extract(tavern_recent.data,'$.files') END)`, s.key(e.Scope), e.MessageID, e.MessageIndex, string(raw), e.CreatedAt.UnixNano(), e.CreatedAt.Add(30*24*time.Hour).Unix())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Recent(ctx context.Context, scope domain.Scope, exclude string, limit int) ([]domain.Event, error) {
	if err := s.checkScope(scope); err != nil {
		return nil, err
	}
	if scope.Kind != "group" || limit < 1 || limit > 20 {
		return nil, errors.New("recent_messages 仅群聊可用，limit 须为 1～20")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM tavern_recent WHERE scope=? AND message<>? AND expires>? ORDER BY created DESC,message DESC LIMIT ?`, s.key(scope), exclude, time.Now().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Event{}
	for rows.Next() {
		var raw string
		var e domain.Event
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func (s *Store) ResolveQuote(ctx context.Context, e domain.Event) (domain.Event, error) {
	if err := s.validEvent(e); err != nil {
		return e, err
	}
	if e.Scope.Kind != "group" || e.ReferencedMessageIndex == "" {
		return e, nil
	}
	for _, q := range e.Quotes {
		if len(q.Images) > 0 || len(q.Files) > 0 {
			return e, nil
		}
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM tavern_recent WHERE scope=? AND (message_index=? OR message=?) AND message<>? AND expires>? ORDER BY created DESC LIMIT 1`, s.key(e.Scope), e.ReferencedMessageIndex, e.ReferencedMessageIndex, e.MessageID, time.Now().Unix()).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return e, nil
	}
	if err != nil {
		return e, err
	}
	var saved domain.Event
	if err = json.Unmarshal([]byte(raw), &saved); err != nil {
		return e, err
	}
	images := saved.Images
	files := saved.Files
	if time.Since(saved.CreatedAt) > time.Hour {
		images = nil
		files = nil
	}
	e.Quotes = append([]domain.Quote(nil), e.Quotes...)
	quote := domain.Quote{AuthorID: saved.UserID, AuthorName: saved.UserName, Text: saved.Text, Images: images, Files: files}
	if len(e.Quotes) == 0 || e.Quotes[0].Text == "" || e.Quotes[0].Text == "[引用消息正文未由 QQ 提供]" {
		e.Quotes = []domain.Quote{quote}
	} else if len(images) > 0 || len(files) > 0 {
		e.Quotes = append(e.Quotes, quote)
	}
	return e, nil
}

func clipRecent(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
