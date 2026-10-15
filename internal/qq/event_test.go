package qq

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/supertx/qq-tavern/internal/domain"
)

func eventJSON(t *testing.T, typ, user string, now time.Time) []byte {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"id": "dispatch-1", "op": 0, "t": typ, "d": map[string]any{"id": "message-1", "group_openid": "group-a", "author": map[string]string{"member_openid": user, "user_openid": user, "username": "同名成员"}, "content": "你好", "timestamp": now.Format(time.RFC3339Nano), "attachments": []map[string]any{{"content_type": "image/png", "url": "//example.com/a.png"}, {"content_type": "voice", "url": "https://example.com/a.mp3"}}, "msg_elements": []map[string]any{{"message_type": 103, "msg_elements": []map[string]any{{"message_type": 0, "author": map[string]string{"member_openid": "quoted-member", "username": "同名成员"}, "content": "引用文本"}, {"message_type": 0, "content": "没有作者"}}}}}})
	return data
}
func TestParseIdentityQuotesAndImages(t *testing.T) {
	now := time.Now()
	a, err := ParseEvent(eventJSON(t, "GROUP_AT_MESSAGE_CREATE", "a", now), domain.Key("app"), now, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseEvent(eventJSON(t, "GROUP_AT_MESSAGE_CREATE", "b", now), domain.Key("app"), now, false)
	if err != nil {
		t.Fatal(err)
	}
	if a.UserName != b.UserName || a.MemberKey() == b.MemberKey() {
		t.Fatal("nickname established identity")
	}
	if len(a.Images) != 1 || a.Images[0] != "https://example.com/a.png" {
		t.Fatalf("images=%v", a.Images)
	}
	if len(a.Quotes) != 2 || a.Quotes[0].AuthorID != "quoted-member" || a.Quotes[1].AuthorID != "" {
		t.Fatalf("quotes=%+v", a.Quotes)
	}
	if a.Deadline.Sub(now) != 285*time.Second {
		t.Fatalf("deadline=%v", a.Deadline)
	}
}
func TestParsePrivateObservationAndDeadline(t *testing.T) {
	now := time.Now()
	e, err := ParseEvent(eventJSON(t, "C2C_MESSAGE_CREATE", "u", now), "bot", now, false)
	if err != nil || e.Scope.Kind != "private" || e.Scope.ID != "u" {
		t.Fatalf("private=%+v err=%v", e, err)
	}
	e, err = ParseEvent(eventJSON(t, "GROUP_MESSAGE_CREATE", "u", now), "bot", now, false)
	if err != nil || e != nil {
		t.Fatal("observation unexpectedly enabled")
	}
	e, err = ParseEvent(eventJSON(t, "GROUP_MESSAGE_CREATE", "u", now), "bot", now, true)
	if err != nil || !e.ObserveOnly {
		t.Fatal("observation should not trigger reply")
	}
	e, err = ParseEvent(eventJSON(t, "GROUP_AT_MESSAGE_CREATE", "u", now.Add(-286*time.Second)), "bot", now, false)
	if err != nil || e != nil {
		t.Fatal("stale dispatch accepted")
	}
	e, err = ParseEvent(eventJSON(t, "GROUP_AT_MESSAGE_CREATE", "u", now.Add(time.Hour)), "bot", now, false)
	if err != nil || !e.Deadline.Equal(now.Add(285*time.Second)) {
		t.Fatal("future timestamp extended deadline")
	}
	raw := strings.ReplaceAll(string(eventJSON(t, "GROUP_AT_MESSAGE_CREATE", "u", now)), `"member_openid":"u"`, `"member_openid":""`)
	raw = strings.ReplaceAll(raw, `"user_openid":"u"`, `"user_openid":""`)
	if _, err = ParseEvent([]byte(raw), "bot", now, false); err == nil {
		t.Fatal("missing sender identity accepted")
	}
}

func TestDispatchIDsDoNotControlReplyDedup(t *testing.T) {
	now := time.Now()
	raw := eventJSON(t, "GROUP_AT_MESSAGE_CREATE", "u", now)
	a, err := ParseEvent(raw, "bot", now, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseEvent([]byte(strings.ReplaceAll(string(raw), "dispatch-1", "dispatch-2")), "bot", now, true)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := ParseEvent(eventJSON(t, "GROUP_MESSAGE_CREATE", "u", now), "bot", now, true)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID || a.ID == observed.ID {
		t.Fatal("dispatch redelivery or observation incorrectly controls reply dedup")
	}
}
