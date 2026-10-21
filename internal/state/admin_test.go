package state

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPolicyInstancePersistenceAndV3Migration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "instance.db")
	s, err := Open(path)
	mustState(t, err)
	id, err := s.InstanceID(ctx)
	mustState(t, err)
	p, err := s.Policy(ctx)
	mustState(t, err)
	if id == "" || p.Version != 0 || len(p.Groups) != 6 {
		t.Fatal(id, p)
	}
	for _, enabled := range p.Groups {
		if enabled {
			t.Fatal("default policy is open")
		}
	}
	_, err = s.SetPolicy(ctx, 0, map[CommandGroup]bool{WorldEdit: true})
	mustState(t, err)
	_, err = s.SetPolicy(ctx, 0, map[CommandGroup]bool{RoleEdit: true})
	if !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	mustState(t, s.Close())
	s, err = Open(path)
	mustState(t, err)
	p, err = s.Policy(ctx)
	mustState(t, err)
	id2, err := s.InstanceID(ctx)
	mustState(t, err)
	if id != id2 || !p.Groups[WorldEdit] || p.Groups[RoleEdit] {
		t.Fatal(id2, p)
	}
	// Simulate the actual v3 schema, retaining the pre-existing role and memory.
	e := testEvent("old", "g", "u")
	view, err := s.BindActive(ctx, e.Scope)
	mustState(t, err)
	_, err = view.UpdatePersonal(ctx, e, 0, "旧记忆")
	mustState(t, err)
	_, err = s.db.Exec(`DROP TABLE tavern_instance; DROP TABLE tavern_command_policy; DROP TABLE tavern_import; DROP TABLE tavern_admin_audit; ALTER TABLE tavern_active_role DROP COLUMN version; PRAGMA user_version=3`)
	mustState(t, err)
	mustState(t, s.Close())
	s, err = Open(path)
	mustState(t, err)
	defer s.Close()
	p, err = s.Policy(ctx)
	mustState(t, err)
	if p.Groups[WorldEdit] || p.Version != 0 {
		t.Fatal("v3 migration did not close commands")
	}
	snap, err := s.Snapshot(ctx, e.Scope, e.UserID)
	mustState(t, err)
	if snap.Personal != "旧记忆" {
		t.Fatal("migration lost data")
	}
}

func TestTransactionPermissionAndUndoDiff(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e := testEvent("setup", "g", "u")
	v, err := s.BindActive(ctx, e.Scope)
	mustState(t, err)
	c, err := v.UpdateConfig(ctx, e, 0, func(c *Config) error {
		c.Character.Name = "A"
		c.Note = "规则"
		return nil
	})
	mustState(t, err)
	records, err := v.Revisions(ctx, e, 10)
	mustState(t, err)
	_, err = s.SetPolicy(ctx, 0, map[CommandGroup]bool{WorldEdit: true, ContextManage: true})
	mustState(t, err)
	e.ID = "undo"
	if err = v.Undo(WithQQCommand(ctx), e, records[0].ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("world-only permission undid a character mutation", err)
	}
	// The command was authorized earlier, but the write must recheck.
	mustState(t, s.CheckCommandGroups(ctx, WorldEdit))
	_, err = s.SetPolicy(ctx, 1, map[CommandGroup]bool{WorldEdit: false})
	mustState(t, err)
	e.ID = "late"
	_, err = v.UpdateConfig(WithCommandGroups(ctx, WorldEdit), e, c.Version, func(c *Config) error { c.Note = "迟到"; return nil })
	if !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	_, err = v.UpdatePersonal(WithQQCommand(ctx), e, 0, "个人")
	if !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err = v.SetStatus(WithQQCommand(ctx), e, "好感度", float64(99), false); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}

func TestImportAtomicPatchPreservesRuntimeAndIdempotency(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	e := testEvent("initial", "g", "alice")
	v, err := s.BindActive(ctx, e.Scope)
	mustState(t, err)
	_, err = v.UpdateConfig(ctx, e, 0, func(c *Config) error {
		c.Character = Character{Name: "A", Description: "保留描述", Personality: "保留性格"}
		c.Note = "保留规则"
		c.Entries = []Entry{{ID: 9, Title: "旧", Content: "旧内容", Constant: true, Enabled: true}}
		return nil
	})
	mustState(t, err)
	e.ID = "personal"
	_, err = v.UpdatePersonal(ctx, e, 0, "成员记忆")
	mustState(t, err)
	e.ID = "state"
	mustState(t, v.SetStatus(ctx, e, "好感度", float64(42), false))
	e.ID = "turn"
	mustState(t, v.AddTurn(ctx, e, "m", json.RawMessage(`[]`)))
	e.ID = "summary"
	mustState(t, v.SetSummary(ctx, e, 0, "故事摘要"))
	bundle := []byte(`{"format":"qq-tavern.bundle","version":1,"roles":[{"name":"A","character":{"scenario":"新场景"},"worldbook":{"entries":[{"title":"世界观","content":"新世界","constant":true}]}},{"name":"B","character":{"description":"新角色"}}]}`)
	p, err := s.PreviewImport(ctx, e.Scope, "admin-api", "admin", bundle)
	mustState(t, err)
	if len(p.Changes) != 2 || p.Changes[0].Action != "update" || p.Changes[1].Action != "create" {
		t.Fatal(p)
	}
	result, err := s.CommitImport(ctx, e.Scope, "admin-api", "admin", p.ID)
	mustState(t, err)
	again, err := s.CommitImport(ctx, e.Scope, "admin-api", "admin", p.ID)
	mustState(t, err)
	if !reflect.DeepEqual(result, again) {
		t.Fatal("repeated confirmation changed receipt")
	}
	snap, err := v.Snapshot(ctx, e.Scope, "alice")
	mustState(t, err)
	if snap.Config.Character.Description != "保留描述" || snap.Config.Character.Personality != "保留性格" || snap.Config.Character.Scenario != "新场景" || snap.Config.Note != "保留规则" || snap.Config.Version != 2 || snap.Personal != "成员记忆" || snap.Summary.Text != "故事摘要" || affection(t, v, "g", "alice") != 42 {
		t.Fatalf("partial import lost data: %+v", snap)
	}
	if len(snap.Config.Entries) != 1 || snap.Config.Entries[0].ID != 1 || !snap.Config.Entries[0].Enabled || snap.Config.Entries[0].Priority != 50 {
		t.Fatal(snap.Config.Entries)
	}
	active, err := s.BindActive(ctx, e.Scope)
	mustState(t, err)
	if active.Role().ID != v.Role().ID {
		t.Fatal("import switched active role")
	}
	history, err := v.History(ctx, e.Scope, 20)
	mustState(t, err)
	if len(history) != 1 {
		t.Fatal("lost history")
	}
	// The first update succeeds inside the transaction, but a later create
	// conflicts; both must roll back.
	conflictBundle := []byte(`{"format":"qq-tavern.bundle","version":1,"roles":[{"name":"A","note":"不可保存"},{"name":"C"}]}`)
	p, err = s.PreviewImport(ctx, e.Scope, "admin-api", "admin", conflictBundle)
	mustState(t, err)
	_, err = s.AdminCreateRole(ctx, e.Scope, "C")
	mustState(t, err)
	if _, err = s.CommitImport(ctx, e.Scope, "admin-api", "admin", p.ID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	c, err := v.RoleConfig(ctx)
	mustState(t, err)
	if c.Note != "保留规则" || c.Version != 2 {
		t.Fatal("partial commit", c)
	}
}

func TestImportPermissionsOwnershipExpiryAndVersionConflict(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	e := testEvent("initial", "g", "alice")
	v, err := s.BindActive(ctx, e.Scope)
	mustState(t, err)
	data := []byte(`{"format":"qq-tavern.bundle","version":1,"roles":[{"name":"默认角色","note":"规则"}]}`)
	if _, err = s.PreviewImport(ctx, e.Scope, "alice", "qq", data); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	_, err = s.SetPolicy(ctx, 0, map[CommandGroup]bool{WorldEdit: true})
	mustState(t, err)
	p, err := s.PreviewImport(ctx, e.Scope, "alice", "qq", data)
	mustState(t, err)
	for _, args := range [][2]string{{"bob", "qq"}, {"alice", "admin"}} {
		if _, err = s.CommitImport(ctx, e.Scope, args[0], args[1], p.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("owner/source bypass", err)
		}
	}
	other := e.Scope
	other.ID = "other"
	if _, err = s.CommitImport(ctx, other, "alice", "qq", p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("scope bypass", err)
	}
	_, err = s.SetPolicy(ctx, 1, map[CommandGroup]bool{WorldEdit: false})
	mustState(t, err)
	if _, err = s.CommitImport(ctx, e.Scope, "alice", "qq", p.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("permission not rechecked", err)
	}
	_, err = s.SetPolicy(ctx, 2, map[CommandGroup]bool{WorldEdit: true})
	mustState(t, err)
	_, err = v.UpdateConfig(ctx, e, 0, func(c *Config) error { c.Note = "另一修改"; return nil })
	mustState(t, err)
	if _, err = s.CommitImport(ctx, e.Scope, "alice", "qq", p.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("version not rechecked", err)
	}
	p, err = s.PreviewImport(ctx, e.Scope, "alice", "qq", data)
	mustState(t, err)
	_, err = s.db.Exec(`UPDATE tavern_import SET expires=0 WHERE id=?`, p.ID)
	mustState(t, err)
	if _, err = s.CommitImport(ctx, e.Scope, "alice", "qq", p.ID); !errors.Is(err, ErrExpiredImport) {
		t.Fatal(err)
	}
	p, err = s.PreviewImport(ctx, e.Scope, "alice", "qq", data)
	mustState(t, err)
	mustState(t, s.CancelImport(ctx, e.Scope, "alice", "qq", p.ID))
	if _, err = s.CommitImport(ctx, e.Scope, "alice", "qq", p.ID); !errors.Is(err, ErrExpiredImport) {
		t.Fatal(err)
	}
}

func TestBundleStrictValidation(t *testing.T) {
	for _, data := range []string{
		`{"format":"qq-tavern.bundle","version":2,"roles":[{"name":"A"}]}`,
		`{"format":"qq-tavern.bundle","version":1,"roles":[{"name":"A","name":"B"}]}`,
		`{"format":"qq-tavern.bundle","version":1,"roles":[{"name":"A","note":null}]}`,
		`{"format":"qq-tavern.bundle","version":1,"roles":[{"name":"A","status":{}}]}`,
		`{"format":"qq-tavern.bundle","version":1,"roles":[{"name":"A"},{"name":"A"}]}`,
		`{"format":"qq-tavern.bundle","version":1,"roles":[{"name":"A","character":{"name":"B"}}]}`,
		`{"format":"qq-tavern.bundle","version":1,"roles":[]} {}`,
		strings.Repeat("x", MaxImportBytes+1),
	} {
		if _, err := ParseBundle([]byte(data)); err == nil {
			t.Fatal("accepted invalid bundle")
		}
	}
	ctx := context.Background()
	s := testStore(t)
	e := testEvent("e", "g", "u")
	_, err := s.BindActive(ctx, e.Scope)
	mustState(t, err)
	_, err = s.PreviewImport(ctx, e.Scope, "admin-api", "admin", []byte(`{"format":"qq-tavern.bundle","version":1,"roles":[{"name":"A","worldbook":{"entries":[{"title":"bad","content":"","constant":true}]}}]}`))
	if err == nil || !strings.Contains(err.Error(), "$.roles[0]") || !strings.Contains(err.Error(), "entries[0]") {
		t.Fatal("missing validation path", err)
	}
	roles, err := s.Roles(ctx, e.Scope)
	mustState(t, err)
	if len(roles) != 1 {
		t.Fatal("validation wrote roles")
	}
	_, err = ParseBundle([]byte(`{"format":"qq-tavern.bundle","version":1,"roles":[{"name":"A","worldbook":{"entries":[{"title":"world","content":"x","constant":true,"id":1}]}}]}`))
	if err == nil || !strings.Contains(err.Error(), "$.roles[0].worldbook.entries[0].id") {
		t.Fatal("unknown field did not identify the item", err)
	}
}

func TestDocumentedBundleAndEmptyWorldbook(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "examples", "tavern.bundle.json"))
	mustState(t, err)
	s := testStore(t)
	ctx := context.Background()
	e := testEvent("example", "g", "u")
	_, err = s.BindActive(ctx, e.Scope)
	mustState(t, err)
	p, err := s.PreviewImport(ctx, e.Scope, "admin-api", "admin", data)
	mustState(t, err)
	_, err = s.CommitImport(ctx, e.Scope, "admin-api", "admin", p.ID)
	mustState(t, err)
	v, err := s.BindRole(ctx, e.Scope, "星灯")
	mustState(t, err)
	before, err := v.RoleConfig(ctx)
	mustState(t, err)
	if len(before.Entries) != 2 || before.Budget != 6000 {
		t.Fatal(before)
	}
	_, err = s.SetPolicy(ctx, 0, map[CommandGroup]bool{WorldEdit: true})
	mustState(t, err)
	// An empty character object changes nothing and must not require role_edit.
	p, err = s.PreviewImport(ctx, e.Scope, "u", "qq", []byte(`{"format":"qq-tavern.bundle","version":1,"roles":[{"name":"星灯","character":{},"worldbook":{"entries":[]}}]}`))
	mustState(t, err)
	_, err = s.CommitImport(ctx, e.Scope, "u", "qq", p.ID)
	mustState(t, err)
	after, err := v.RoleConfig(ctx)
	mustState(t, err)
	if len(after.Entries) != 0 || after.Character != before.Character || after.Note != before.Note {
		t.Fatal("empty entries changed unrelated fields", after)
	}
}
