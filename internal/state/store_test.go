package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/supertx/qq-tavern/internal/domain"
)

func testEvent(id, group, user string) domain.Event {
	return domain.Event{ID: id, MessageID: id, Scope: domain.Scope{BotID: "bot", Kind: "group", ID: group}, UserID: user, UserName: "相同昵称", CreatedAt: time.Now(), Deadline: time.Now().Add(time.Minute)}
}
func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestIsolationPersistenceCASAndDuplicate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	e := testEvent("a", "one", "alice")
	config, err := s.UpdateConfig(ctx, e, 0, func(c *Config) error { c.Note = "群规则"; return nil })
	if err != nil || config.Version != 1 {
		t.Fatalf("%+v %v", config, err)
	}
	_, err = s.UpdateConfig(ctx, e, 0, func(c *Config) error { t.Fatal("duplicate event changed state"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	e.ID = "b"
	if _, err = s.UpdateConfig(ctx, e, 0, func(c *Config) error { return nil }); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if _, err = s.UpdatePersonal(ctx, e, 0, "alice secret"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap, err := s.Snapshot(ctx, e.Scope, "alice")
	if err != nil || snap.Personal != "alice secret" || snap.Config.Note != "群规则" {
		t.Fatalf("restart: %+v %v", snap, err)
	}
	snap, err = s.Snapshot(ctx, e.Scope, "bob")
	if err != nil || snap.Personal != "" || snap.Config.Note != "群规则" {
		t.Fatalf("member isolation: %+v %v", snap, err)
	}
	other := e.Scope
	other.ID = "two"
	snap, err = s.Snapshot(ctx, other, "alice")
	if err != nil || snap.Personal != "" || snap.Config.Note != "" {
		t.Fatal("group leak")
	}
	other = e.Scope
	other.BotID = "another"
	snap, err = s.Snapshot(ctx, other, "alice")
	if err != nil || snap.Personal != "" || snap.Config.Note != "" {
		t.Fatal("bot leak")
	}
	other = domain.Scope{BotID: "bot", Kind: "private", ID: "alice"}
	if _, err = s.Snapshot(ctx, other, "bob"); err == nil {
		t.Fatal("private scope accepted other member")
	}
}
func TestPersonalRevisionPrivacyAndUndo(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	alice := testEvent("a", "one", "alice")
	bob := testEvent("b", "one", "bob")
	if _, err := s.UpdatePersonal(ctx, alice, 0, "secret"); err != nil {
		t.Fatal(err)
	}
	revisions, err := s.Revisions(ctx, alice, 20)
	if err != nil || len(revisions) != 1 {
		t.Fatal(err, revisions)
	}
	secretID := revisions[0].ID
	visible, err := s.Revisions(ctx, bob, 20)
	if err != nil || len(visible) != 0 {
		t.Fatal("other member's revisions leaked")
	}
	if err = s.Undo(ctx, bob, secretID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("undo privacy: %v", err)
	}
	alice.ID = "undo"
	if err = s.Undo(ctx, alice, secretID); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(ctx, alice.Scope, "alice")
	if snap.Personal != "" || snap.PersonalVersion != 2 {
		t.Fatalf("undo: %+v", snap)
	}
	alice.ID = "public"
	if _, err = s.UpdateConfig(ctx, alice, 0, func(c *Config) error { c.Note = "shared"; return nil }); err != nil {
		t.Fatal(err)
	}
	visible, err = s.Revisions(ctx, bob, 20)
	if err != nil || len(visible) != 1 {
		t.Fatal(err, visible)
	}
	bob.ID = "public-undo"
	if err = s.Undo(ctx, bob, visible[0].ID); err != nil {
		t.Fatal(err)
	}
	alice.ID = "stale-undo"
	if err = s.Undo(ctx, alice, visible[0].ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale undo: %v", err)
	}
}
func TestConcurrentConfigCAS(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := testEvent(fmt.Sprint(i), "one", fmt.Sprint(i))
			_, err := s.UpdateConfig(ctx, e, 0, func(c *Config) error { c.Note = fmt.Sprint(i); return nil })
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success %d conflict %d", success, conflict)
	}
}
func TestHistorySummaryCheckpointsAndReset(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e := testEvent("observe", "one", "alice")
	e.ObserveOnly = true
	for i := 0; i < 4; i++ {
		e.ID = fmt.Sprintf("observe%d", i)
		e.MessageID = e.ID
		if err := s.AddTurn(ctx, e, "m", json.RawMessage(`{"messages":[{"role":"user","content":"观察"}]}`)); err != nil {
			t.Fatal(err)
		}
	}
	due, err := s.DueSummaries(ctx, time.Now())
	if err != nil || len(due) != 0 {
		t.Fatal("observations counted as completed turns", due, err)
	}
	e.ObserveOnly = false
	for i := 0; i < 3; i++ {
		e.ID = fmt.Sprint(i)
		e.MessageID = e.ID
		if err = s.AddTurn(ctx, e, "m", json.RawMessage(`{"messages":[{"role":"assistant","content":"回复"}]}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.AddTurn(ctx, e, "m", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	turns, err := s.History(ctx, e.Scope, 100)
	if err != nil || len(turns) != 7 {
		t.Fatalf("history dedup %d %v", len(turns), err)
	}
	due, err = s.DueSummaries(ctx, time.Now())
	if err != nil || len(due) != 1 {
		t.Fatal(due, err)
	}
	batch, err := s.HistorySince(ctx, e.Scope, 0, 2)
	if err != nil || len(batch) != 2 || batch[1].ID != turns[1].ID {
		t.Fatal("summary batch skips turns")
	}
	if err = s.SaveSummary(ctx, e.Scope, 0, batch[1].ID, "first"); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveSummary(ctx, e.Scope, 0, turns[6].ID, "stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("summary conflict: %v", err)
	}
	other := e.Scope
	other.ID = "other"
	if err = s.SaveSummary(ctx, other, 0, turns[6].ID, "leak"); err == nil {
		t.Fatal("cross-scope checkpoint accepted")
	}
	e.ID = "config"
	if _, err = s.UpdateConfig(ctx, e, 0, func(c *Config) error { c.Note = "persist"; return nil }); err != nil {
		t.Fatal(err)
	}
	e.ID = "reset"
	if err = s.ResetContext(ctx, e); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(ctx, e.Scope, "alice")
	turns, _ = s.History(ctx, e.Scope, 10)
	if snap.Config.Note != "persist" || snap.Summary.Text != "" || len(turns) != 0 {
		t.Fatal("reset deleted memories or left context")
	}
}

func TestFutureSchemaRejectedWithoutDowngrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec(`PRAGMA user_version=3`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(path); err == nil {
		reopened.Close()
		t.Fatal("future database was accepted")
	}
}

func TestDuplicateResetKeepsLaterTurns(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e := testEvent("reset", "group", "alice")
	if err := s.ResetContext(ctx, e); err != nil {
		t.Fatal(err)
	}
	turn := e
	turn.ID = "later"
	if err := s.AddTurn(ctx, turn, "m", json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetContext(ctx, e); err != nil {
		t.Fatal(err)
	}
	history, err := s.History(ctx, e.Scope, 10)
	if err != nil || len(history) != 1 {
		t.Fatal("duplicate reset destroyed newer turns", history, err)
	}
}

func TestRepliedMessageReplacesObservationAndAdvancesCheckpoint(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	observed := testEvent("observed-event", "group", "alice")
	observed.MessageID = "same-qq-message"
	observed.ObserveOnly = true
	if err := s.AddTurn(ctx, observed, "m", json.RawMessage(`[{"role":"user","content":"hello"}]`)); err != nil {
		t.Fatal(err)
	}
	turns, _ := s.History(ctx, observed.Scope, 10)
	oldID := turns[0].ID
	if err := s.SaveSummary(ctx, observed.Scope, 0, oldID, "Alice said hello"); err != nil {
		t.Fatal(err)
	}
	replied := observed
	replied.ID = "replied-event"
	replied.ObserveOnly = false
	payload := json.RawMessage(`[{"role":"user","content":"hello"},{"role":"assistant","content":"world"}]`)
	if err := s.AddTurn(ctx, replied, "m", payload); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTurn(ctx, observed, "m", json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTurn(ctx, replied, "m", json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	turns, err := s.History(ctx, observed.Scope, 10)
	if err != nil || len(turns) != 1 || turns[0].ID <= oldID || turns[0].Event.ObserveOnly || string(turns[0].Payload) != string(payload) {
		t.Fatalf("replied event not preserved: %+v %v", turns, err)
	}
	incremental, err := s.HistorySince(ctx, observed.Scope, oldID, 10)
	if err != nil || len(incremental) != 1 {
		t.Fatal("new reply skipped summary checkpoint", incremental, err)
	}
	other := observed
	other.Scope.ID = "other"
	if err = s.AddTurn(ctx, other, "m", json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	otherTurns, _ := s.History(ctx, other.Scope, 10)
	if len(otherTurns) != 1 {
		t.Fatal("source message dedup crossed scope")
	}
}
