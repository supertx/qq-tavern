package state

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func mustState(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func affection(t *testing.T, s *Store, group, user string) float64 {
	t.Helper()
	snap, err := s.Snapshot(context.Background(), testEvent("read", group, user).Scope, user)
	mustState(t, err)
	d, _ := snap.Status.Definition("好感度")
	return snap.Status.Value(d).(float64)
}

func TestRoleNamespacesPersistAllMemoryAndStatus(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "roles.db")
	s, err := Open(path)
	mustState(t, err)
	e := testEvent("config", "g", "alice")
	a, err := s.BindActive(ctx, e.Scope)
	mustState(t, err)
	_, err = a.UpdateConfig(ctx, e, 0, func(c *Config) error {
		c.Character.Name = "丛雨"
		c.Note = "A规则"
		c.Entries = []Entry{{ID: 1, Title: "神刀", Content: "A世界书", Constant: true, Enabled: true, Priority: 50}}
		return nil
	})
	mustState(t, err)
	_, err = a.UpdatePersonal(ctx, testEvent("personal", "g", "alice"), 0, "A个人记忆")
	mustState(t, err)
	mustState(t, a.AddTurn(ctx, testEvent("turn", "g", "alice"), "model", json.RawMessage(`[{"role":"user","content":"A历史"}]`)))
	h, err := a.History(ctx, e.Scope, 20)
	mustState(t, err)
	mustState(t, a.SaveSummary(ctx, e.Scope, 0, h[0].ID, "A摘要"))
	mustState(t, a.RecordActivation(ctx, e, []Activation{{ID: 1, Injected: true}}))
	mustState(t, a.SetStatus(ctx, testEvent("score", "g", "alice"), "好感度", float64(37), false))
	mustState(t, s.SelectRole(ctx, testEvent("create", "g", "bob"), "芳乃", true))
	b, err := s.BindActive(ctx, e.Scope)
	mustState(t, err)
	snap, err := b.Snapshot(ctx, e.Scope, "alice")
	mustState(t, err)
	if snap.Config.Character.Name != "芳乃" || snap.Config.Note != "" || len(snap.Config.Entries) != 0 || snap.Personal != "" || snap.Summary.Text != "" || len(snap.LastActivation) != 0 {
		t.Fatalf("role leaked: %+v", snap)
	}
	h, err = b.History(ctx, e.Scope, 20)
	mustState(t, err)
	if len(h) != 0 || affection(t, b, "g", "alice") != 20 || affection(t, a, "g", "bob") != 20 {
		t.Fatal("history or member state leaked")
	}
	mustState(t, b.SetStatus(ctx, testEvent("bscore", "g", "alice"), "好感度", float64(71), false))
	_, err = a.UpdateConfig(ctx, testEvent("rename", "g", "alice"), 1, func(c *Config) error { c.Character.Name = "丛雨大人"; return nil })
	mustState(t, err)
	if _, err = a.Snapshot(ctx, testEvent("x", "other", "alice").Scope, "alice"); err == nil {
		t.Fatal("bound view accepted other conversation")
	}
	_, err = a.UpdateConfig(ctx, testEvent("collision", "g", "alice"), 2, func(c *Config) error { c.Character.Name = "芳乃"; return nil })
	if err == nil {
		t.Fatal("duplicate role name accepted")
	}
	mustState(t, s.Close())
	s, err = Open(path)
	mustState(t, err)
	defer s.Close()
	active, err := s.BindActive(ctx, e.Scope)
	mustState(t, err)
	if active.Role().Name != "芳乃" || affection(t, active, "g", "alice") != 71 {
		t.Fatal("active role lost after restart")
	}
	a, err = s.BindRole(ctx, e.Scope, "丛雨大人")
	mustState(t, err)
	snap, err = a.Snapshot(ctx, e.Scope, "alice")
	mustState(t, err)
	if snap.Personal != "A个人记忆" || snap.Summary.Text != "A摘要" || snap.Config.Note != "A规则" || len(snap.LastActivation) != 1 || affection(t, a, "g", "alice") != 37 {
		t.Fatal("A was not restored")
	}
	mustState(t, a.ResetContext(ctx, testEvent("reset", "g", "alice")))
	if affection(t, a, "g", "alice") != 37 {
		t.Fatal("context reset erased status")
	}
	h, err = a.Search(ctx, e.Scope, "A历史", 10)
	mustState(t, err)
	if len(h) != 0 {
		t.Fatal("reset left searchable history")
	}
}

func TestStatusStagesAtomicallyDeduplicatesAndChecksOwners(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	e := testEvent("turn", "g", "alice")
	a, err := s.BindActive(ctx, e.Scope)
	mustState(t, err)
	snap, err := a.Snapshot(ctx, e.Scope, e.UserID)
	mustState(t, err)
	delta := 3.0
	text := "开心"
	p, err := PrepareStatus(snap.Status, StatusPatch{Changes: []StatusChange{{Name: "好感度", Delta: &delta}, {Name: "心情", Value: &text}}, Reason: "遵守了约定"})
	mustState(t, err)
	if affection(t, a, "g", "alice") != 20 {
		t.Fatal("staging wrote state")
	}
	mustState(t, a.AddTurnWithStatus(ctx, e, "m", json.RawMessage(`[]`), p))
	mustState(t, a.AddTurnWithStatus(ctx, e, "m", json.RawMessage(`[]`), p))
	if affection(t, a, "g", "alice") != 23 || affection(t, a, "g", "bob") != 20 {
		t.Fatal("duplicate or cross-member score")
	}
	records, err := a.StatusRecords(ctx, testEvent("read", "g", "bob"))
	mustState(t, err)
	if len(records) != 1 || !records[0].Shared {
		t.Fatal("other member saw private audit")
	}
	mustState(t, s.SelectRole(ctx, testEvent("create", "g", "alice"), "B", true))
	b, err := s.BindActive(ctx, e.Scope)
	mustState(t, err)
	if err = b.AddTurnWithStatus(ctx, testEvent("bad", "g", "alice"), "m", json.RawMessage(`[]`), p); err == nil {
		t.Fatal("cross-role patch accepted")
	}
	h, err := b.History(ctx, e.Scope, 20)
	mustState(t, err)
	if len(h) != 0 {
		t.Fatal("failed status commit left a turn")
	}
	snap, err = a.Snapshot(ctx, e.Scope, e.UserID)
	mustState(t, err)
	bad := 4.0
	if _, err = PrepareStatus(snap.Status, StatusPatch{Changes: []StatusChange{{Name: "好感度", Delta: &bad}}, Reason: "大幅变化"}); err == nil {
		t.Fatal("oversize delta accepted")
	}
	if _, err = PrepareStatus(snap.Status, StatusPatch{Changes: []StatusChange{{Name: "未知", Delta: &delta}}, Reason: "变化"}); err == nil {
		t.Fatal("unknown field accepted")
	}
	mustState(t, a.SetStatus(ctx, testEvent("nearmax", "g", "alice"), "好感度", float64(99), false))
	snap, err = a.Snapshot(ctx, e.Scope, e.UserID)
	mustState(t, err)
	p, err = PrepareStatus(snap.Status, StatusPatch{Changes: []StatusChange{{Name: "好感度", Delta: &delta}}, Reason: "善意互动"})
	mustState(t, err)
	mustState(t, a.AddTurnWithStatus(ctx, testEvent("cap", "g", "alice"), "m", json.RawMessage(`[]`), p))
	if affection(t, a, "g", "alice") != 100 {
		t.Fatal("range cap failed")
	}
	snap, err = a.Snapshot(ctx, e.Scope, e.UserID)
	mustState(t, err)
	delta = -1
	p, err = PrepareStatus(snap.Status, StatusPatch{Changes: []StatusChange{{Name: "好感度", Delta: &delta}}, Reason: "失约"})
	mustState(t, err)
	mustState(t, a.SetStatus(ctx, testEvent("intervene", "g", "alice"), "好感度", float64(50), false))
	if err = a.AddTurnWithStatus(ctx, testEvent("stale", "g", "alice"), "m", json.RawMessage(`[]`), p); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale state: %v", err)
	}
}

func TestCustomDefinitionsUndoAndMemberPrivacy(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	e := testEvent("init", "g", "alice")
	v, err := s.BindActive(ctx, e.Scope)
	mustState(t, err)
	mustState(t, v.UpdateStatusSchema(ctx, e, 0, func(c *StatusSchema) error {
		c.Definitions = append(c.Definitions, StatusDefinition{Name: "信任度", Kind: "number", Owner: "member", Initial: float64(10), Min: 0, Max: 100, MaxDelta: 2, Enabled: true})
		return nil
	}))
	mustState(t, v.SetStatus(ctx, testEvent("score", "g", "bob"), "信任度", float64(80), false))
	change := func(c *StatusSchema) error { c.Definitions[3].Max = 50; return nil }
	if err = v.UpdateStatusSchema(ctx, testEvent("narrow", "g", "alice"), 1, change); err == nil {
		t.Fatal("narrowed range past saved member value")
	}
	mustState(t, v.UpdateStatusSchema(ctx, testEvent("disable", "g", "alice"), 1, func(c *StatusSchema) error {
		c.Definitions[3].Enabled = false
		c.Definitions[3].Initial = float64(15)
		return nil
	}))
	snap, err := v.Snapshot(ctx, e.Scope, "bob")
	mustState(t, err)
	d, _ := snap.Status.Definition("信任度")
	if snap.Status.Value(d) != float64(80) {
		t.Fatal("changing default erased saved value")
	}
	delta := 1.0
	if _, err = PrepareStatus(snap.Status, StatusPatch{Changes: []StatusChange{{Name: "信任度", Delta: &delta}}, Reason: "互动"}); err == nil {
		t.Fatal("disabled state updated automatically")
	}
	rows, err := v.StatusRecords(ctx, testEvent("read", "g", "bob"))
	mustState(t, err)
	id := rows[0].ID
	if err = v.Undo(ctx, testEvent("guess", "g", "alice"), id); !errors.Is(err, ErrNotFound) {
		t.Fatal("other member undid private state", err)
	}
	mustState(t, v.Undo(ctx, testEvent("undo", "g", "bob"), id))
	snap, err = v.Snapshot(ctx, e.Scope, "bob")
	mustState(t, err)
	if snap.Status.Value(d) != float64(10) {
		t.Fatal("undo failed to restore original initial value")
	}
	if err = v.Undo(ctx, testEvent("again", "g", "bob"), id); !errors.Is(err, ErrConflict) {
		t.Fatal("outdated undo accepted", err)
	}
}

func TestV1MigrationPreservesLegacyAndVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	mustState(t, err)
	e := testEvent("config", "g", "alice")
	_, err = s.UpdateConfig(ctx, e, 0, func(c *Config) error { c.Character.Name = "丛雨"; c.Note = "旧规则"; return nil })
	mustState(t, err)
	_, err = s.UpdatePersonal(ctx, testEvent("personal", "g", "alice"), 0, "旧个人记忆")
	mustState(t, err)
	mustState(t, s.AddTurn(ctx, testEvent("old", "g", "alice"), "m", json.RawMessage(`[]`)))
	// Recreate the exact v1 boundary, including old revision rows without reason.
	_, err = s.DB().Exec(`DROP TABLE tavern_message_role; DROP TABLE tavern_active_role; DROP TABLE tavern_role; DROP TABLE tavern_status; DROP TABLE tavern_status_schema; ALTER TABLE tavern_revision DROP COLUMN reason; PRAGMA user_version=1;`)
	mustState(t, err)
	mustState(t, s.Close())
	s, err = Open(path)
	mustState(t, err)
	v, err := s.BindActive(ctx, e.Scope)
	mustState(t, err)
	snap, err := v.Snapshot(ctx, e.Scope, "alice")
	mustState(t, err)
	if v.Role().Name != "丛雨" || snap.Config.Note != "旧规则" || snap.Personal != "旧个人记忆" || affection(t, v, "g", "alice") != 20 {
		t.Fatal("legacy migration changed data")
	}
	h, err := v.History(ctx, e.Scope, 20)
	mustState(t, err)
	if len(h) != 1 {
		t.Fatal("lost legacy turn")
	}
	var version int
	mustState(t, s.DB().QueryRow(`PRAGMA user_version`).Scan(&version))
	if version != 2 {
		t.Fatal(version)
	}
	mustState(t, s.Close())
	s, err = Open(path)
	mustState(t, err)
	defer s.Close()
	roles, err := s.Roles(ctx, e.Scope)
	mustState(t, err)
	if len(roles) != 1 {
		t.Fatal("migration repeated")
	}
}

func TestObservedMessageAndSummaryRemainPinnedToRole(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	e := testEvent("observed", "g", "alice")
	e.ObserveOnly = true
	a, err := s.BindMessage(ctx, e)
	mustState(t, err)
	mustState(t, a.AddTurn(ctx, e, "m", json.RawMessage(`[]`)))
	mustState(t, s.SelectRole(ctx, testEvent("switch", "g", "alice"), "B", true))
	e.ID = "reply"
	e.ObserveOnly = false
	again, err := s.BindMessage(ctx, e)
	mustState(t, err)
	if again.Role().ID != a.Role().ID {
		t.Fatal("observation moved roles")
	}
	mustState(t, again.AddTurn(ctx, e, "m", json.RawMessage(`[]`)))
	roles, err := s.DueRoleSummaries(ctx, time.Now().Add(3*time.Minute))
	mustState(t, err)
	if len(roles) != 1 || roles[0].ID != a.Role().ID {
		t.Fatal("summary targets active role instead of historical role")
	}
}

func TestRenameUndoRestoresProfileNameEvenAfterPromptClear(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	e := testEvent("name", "g", "alice")
	v, err := s.BindActive(ctx, e.Scope)
	mustState(t, err)
	_, err = v.UpdateConfig(ctx, e, 0, func(c *Config) error { c.Character.Name = "丛雨"; return nil })
	mustState(t, err)
	revisions, err := v.Revisions(ctx, e, 10)
	mustState(t, err)
	mustState(t, v.Undo(ctx, testEvent("undo", "g", "alice"), revisions[0].ID))
	active, err := s.BindActive(ctx, e.Scope)
	mustState(t, err)
	if active.Role().Name != "默认角色" {
		t.Fatal("initial rename undo left renamed profile")
	}
	_, err = v.UpdateConfig(ctx, testEvent("rename", "g", "alice"), 2, func(c *Config) error { c.Character.Name = "丛雨"; return nil })
	mustState(t, err)
	_, err = v.UpdateConfig(ctx, testEvent("clear", "g", "alice"), 3, func(c *Config) error { c.Character = Character{}; return nil })
	mustState(t, err)
	_, err = v.UpdateConfig(ctx, testEvent("second-name", "g", "alice"), 4, func(c *Config) error { c.Character.Name = "芳乃"; return nil })
	mustState(t, err)
	revisions, err = v.Revisions(ctx, e, 10)
	mustState(t, err)
	mustState(t, v.Undo(ctx, testEvent("second-undo", "g", "alice"), revisions[0].ID))
	active, err = s.BindActive(ctx, e.Scope)
	mustState(t, err)
	if active.Role().Name != "丛雨" {
		t.Fatal("cleared prompt lost its profile name on undo")
	}
}
