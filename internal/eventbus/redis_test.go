package eventbus

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/supertx/qq-tavern/internal/domain"
)

func redisForTest(t *testing.T) string {
	t.Helper()
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		binary = "/opt/homebrew/bin/redis-server"
		if _, err = exec.LookPath(binary); err != nil {
			t.Skip("redis-server is not installed")
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cmd := exec.Command(binary, "--bind", "127.0.0.1", "--port", fmt.Sprint(port), "--save", "", "--appendonly", "no", "--dir", t.TempDir(), "--logfile", filepath.Join(t.TempDir(), "redis.log"))
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	endpoint := fmt.Sprintf("redis://127.0.0.1:%d/0", port)
	b, _ := New(endpoint, "bot")
	for i := 0; i < 100; i++ {
		if b.Ping(context.Background()) == nil {
			return endpoint
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Redis failed to start")
	return ""
}
func busEvent(bot, groupID, message string) domain.Event {
	now := time.Now()
	return domain.Event{ID: message, MessageID: message, Scope: domain.Scope{BotID: bot, Kind: "group", ID: groupID}, UserID: "member", Text: "hello", CreatedAt: now, Deadline: now.Add(285 * time.Second)}
}
func TestBotRoutingDedupAndAck(t *testing.T) {
	endpoint := redisForTest(t)
	a, _ := New(endpoint, "a")
	b, _ := New(endpoint, "b")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e := busEvent("a", "group", "message")
	if err := a.Publish(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := a.Publish(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(ctx, e); err == nil {
		t.Fatal("cross-bot publish allowed")
	}
	d, err := a.Read(ctx, "worker-a")
	if err != nil || d == nil || d.Event.MessageID != "message" {
		t.Fatalf("delivery=%+v err=%v", d, err)
	}
	if err = a.Ack(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if d, err = a.Read(ctx, "worker-a"); err != nil || d != nil {
		t.Fatalf("duplicate=%v err=%v", d, err)
	}
	if d, err = b.Read(ctx, "worker-b"); err != nil || d != nil {
		t.Fatalf("cross-bot delivery=%v err=%v", d, err)
	}
}
func TestRestartDoesNotReplayActiveToolAndReclaimsExpired(t *testing.T) {
	endpoint := redisForTest(t)
	b, _ := New(endpoint, "a")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.Publish(ctx, busEvent("a", "g", "one")); err != nil {
		t.Fatal(err)
	}
	d, err := b.Read(ctx, "old-worker")
	if err != nil || d == nil {
		t.Fatal(err)
	}
	if got, err := b.Read(ctx, "replacement-worker"); err != nil || got != nil {
		t.Fatalf("active event replayed: %v %v", got, err)
	}
	if _, err = b.command(ctx, "XCLAIM", b.stream, group, "old-worker", "0", d.ID, "IDLE", "300000"); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Read(ctx, "replacement-worker"); err != nil || got != nil {
		t.Fatalf("reclaimed event replayed: %v %v", got, err)
	}
	pending, err := b.command(ctx, "XPENDING", b.stream, group)
	if err != nil {
		t.Fatal(err)
	}
	if pending.([]any)[0].(int64) != 0 {
		t.Fatalf("pending=%v", pending)
	}
	e := busEvent("a", "g", "stale")
	e.Deadline = time.Now().Add(-time.Second)
	raw, _ := json.Marshal(e)
	if _, err = b.command(ctx, "XADD", b.stream, "*", "event", string(raw)); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Read(ctx, "replacement-worker"); err != nil || got != nil {
		t.Fatalf("stale=%v %v", got, err)
	}
}
func TestRESPValidation(t *testing.T) {
	for _, raw := range []string{"$9000000\r\n", "*999999\r\n", "$3\r\nabcxx", "wat\r\n"} {
		if _, err := readRESP(bufio.NewReader(strings.NewReader(raw)), 0); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	v, err := readRESP(bufio.NewReader(strings.NewReader("*3\r\n$3\r\nabc\r\n:2\r\n$-1\r\n")), 0)
	if err != nil || v.([]any)[0] != "abc" || v.([]any)[1] != int64(2) || v.([]any)[2] != nil {
		t.Fatalf("value=%v err=%v", v, err)
	}
}
