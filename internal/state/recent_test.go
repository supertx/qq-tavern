package state

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/supertx/qq-tavern/internal/domain"
)

func TestRecentIsolationQuoteLookupRetentionAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recent.db")
	s, err := Open(path)
	mustState(t, err)
	defer func() { s.Close() }()
	e := testEvent("original", "g", "alice")
	e.MessageIndex = "idx"
	e.Text = "original"
	e.Images = []string{"https://gchat.qpic.cn/private.png"}
	e.CreatedAt = time.Now()
	view, err := s.BindMessage(ctx, e)
	mustState(t, err)
	mustState(t, view.RecordRecent(ctx, e))
	mustState(t, view.RecordRecent(ctx, e))
	rows, err := view.Recent(ctx, e.Scope, "", 20)
	mustState(t, err)
	if len(rows) != 1 {
		t.Fatal("dedup", rows)
	}
	reply := testEvent("reply", "g", "bob")
	reply.ReferencedMessageIndex = "idx"
	resolved, err := view.ResolveQuote(ctx, reply)
	mustState(t, err)
	if len(resolved.Quotes) != 1 || resolved.Quotes[0].AuthorID != "alice" || len(resolved.Quotes[0].Images) != 1 {
		t.Fatal(resolved)
	}
	other := testEvent("other", "other-group", "bob")
	other.ReferencedMessageIndex = "idx"
	if _, err = view.ResolveQuote(ctx, other); err == nil {
		t.Fatal("cross-group view accepted")
	}
	mustState(t, s.SelectRole(ctx, testEvent("switch", "g", "bob"), "other", true))
	next, err := s.BindActive(ctx, e.Scope)
	mustState(t, err)
	rows, err = next.Recent(ctx, e.Scope, "", 20)
	mustState(t, err)
	if len(rows) != 0 {
		t.Fatal("cross-role recent messages")
	}
	resolved, err = next.ResolveQuote(ctx, reply)
	mustState(t, err)
	if len(resolved.Quotes) != 0 {
		t.Fatal("cross-role quote")
	}
	// A delayed observer retains the role selected at message reception.
	bound, err := s.BindMessage(ctx, e)
	mustState(t, err)
	if bound.Role().ID != view.Role().ID {
		t.Fatal("observation rebound")
	}
	old := e
	old.ID = "old"
	old.MessageID = "old"
	old.CreatedAt = time.Now().Add(-31 * 24 * time.Hour)
	mustState(t, view.RecordRecent(ctx, old))
	_, err = s.DB().Exec(`UPDATE tavern_recent SET created=? WHERE message='original'`, time.Now().Add(-2*time.Hour).UnixNano())
	mustState(t, err)
	fresh := e
	fresh.ID = "fresh"
	fresh.MessageID = "fresh"
	mustState(t, view.RecordRecent(ctx, fresh))
	var raw string
	mustState(t, s.DB().QueryRow(`SELECT data FROM tavern_recent WHERE message='original'`).Scan(&raw))
	if strings.Contains(raw, "private.png") {
		t.Fatal("stale image URL kept")
	}
	rows, err = view.Recent(ctx, e.Scope, "fresh", 20)
	mustState(t, err)
	if len(rows) != 1 {
		t.Fatal("expiry/exclusion", rows)
	}
	roleName := view.Role().Name
	mustState(t, s.Close())
	s, err = Open(path)
	mustState(t, err)
	view, err = s.BindRole(ctx, e.Scope, roleName)
	mustState(t, err)
	rows, err = view.Recent(ctx, e.Scope, "", 20)
	mustState(t, err)
	if len(rows) != 2 {
		t.Fatal("reopen", rows)
	}
	mustState(t, view.ResetContext(ctx, testEvent("reset-recent", "g", "alice")))
	rows, err = view.Recent(ctx, e.Scope, "", 20)
	mustState(t, err)
	if len(rows) != 0 {
		t.Fatal("reset retained recent")
	}
}

func TestRecentRejectsPrivateAndLimit(t *testing.T) {
	s, err := Open(":memory:")
	mustState(t, err)
	defer s.Close()
	for _, limit := range []int{0, 21} {
		if _, err = s.Recent(context.Background(), testEvent("e", "g", "u").Scope, "", limit); err == nil {
			t.Fatal(limit)
		}
	}
	if _, err = s.Recent(context.Background(), domain.Scope{BotID: "bot", Kind: "private", ID: "u"}, "", 1); err == nil {
		t.Fatal("private query accepted")
	}
}
