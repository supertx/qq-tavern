// Package eventbus carries deadline-bound QQ events on a per-bot Redis stream.
package eventbus

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/supertx/qq-tavern/internal/domain"
)

const group = "workers"

type Bus struct {
	endpoint      *url.URL
	botID, stream string
}
type Delivery struct {
	ID    string
	Event domain.Event
}

func New(rawURL, botID string) (*Bus, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "redis" && u.Scheme != "rediss") || u.Hostname() == "" || botID == "" {
		return nil, errors.New("REDIS_URL must be redis(s)://host/db and bot ID must be set")
	}
	if u.Path != "" && u.Path != "/" {
		n, e := strconv.Atoi(strings.TrimPrefix(u.Path, "/"))
		if e != nil || n < 0 {
			return nil, errors.New("invalid Redis database")
		}
	}
	return &Bus{endpoint: u, botID: botID, stream: "qq-tavern:events:" + domain.Key(botID)}, nil
}

func (b *Bus) Publish(ctx context.Context, event domain.Event) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if event.Scope.BotID != b.botID {
		return errors.New("event belongs to another bot")
	}
	if !time.Now().Before(event.Deadline) {
		return nil
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	// Both the dedup marker and stream append commit atomically, including after a lost reply.
	_, err = b.command(ctx, "EVAL", `if redis.call('EXISTS',KEYS[1]) == 1 then return 0 end; redis.call('XADD',KEYS[2],'MAXLEN','~',10000,'*','event',ARGV[1]); redis.call('SET',KEYS[1],'1','EX',600); return 1`, "2", "qq-tavern:seen:"+domain.Key(event.Scope.Key(), event.MessageID, strconv.FormatBool(event.ObserveOnly)), b.stream, string(data))
	return err
}

func (b *Bus) Read(ctx context.Context, consumer string) (*Delivery, error) {
	if consumer == "" {
		return nil, errors.New("consumer name is required")
	}
	if _, err := b.command(ctx, "XGROUP", "CREATE", b.stream, group, "0", "MKSTREAM"); err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return nil, err
	}
	// An interrupted consumer's events are reclaimed only after their entire processing
	// budget expired. Replaying partially executed tools would be unsafe.
	claimed, err := b.command(ctx, "XAUTOCLAIM", b.stream, group, consumer, "285000", "0-0", "COUNT", "100")
	if err != nil {
		return nil, err
	}
	if a, ok := claimed.([]any); ok && len(a) >= 2 {
		if entries, ok := a[1].([]any); ok {
			for _, entry := range entries {
				d, _ := b.decode(entry)
				if d == nil {
					continue
				}
				if err = b.Ack(ctx, d.ID); err != nil {
					return nil, err
				}
			}
		}
	}
	result, err := b.command(ctx, "XREADGROUP", "GROUP", group, consumer, "COUNT", "1", "BLOCK", "1000", "STREAMS", b.stream, ">")
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	streams, ok := result.([]any)
	if !ok || len(streams) == 0 {
		return nil, errors.New("invalid Redis stream reply")
	}
	s, ok := streams[0].([]any)
	if !ok || len(s) != 2 {
		return nil, errors.New("invalid Redis stream")
	}
	entries, ok := s[1].([]any)
	if !ok || len(entries) == 0 {
		return nil, nil
	}
	d, err := b.decode(entries[0])
	if d != nil && (err != nil || !time.Now().Before(d.Event.Deadline)) {
		if ackErr := b.Ack(ctx, d.ID); ackErr != nil {
			return nil, ackErr
		}
		return nil, nil
	}
	return d, err
}

func (b *Bus) decode(value any) (*Delivery, error) {
	a, ok := value.([]any)
	if !ok || len(a) != 2 {
		return nil, errors.New("invalid stream entry")
	}
	id, ok := a[0].(string)
	if !ok {
		return nil, errors.New("invalid stream ID")
	}
	d := &Delivery{ID: id}
	fields, ok := a[1].([]any)
	if !ok {
		return d, errors.New("invalid stream fields")
	}
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] == "event" {
			raw, ok := fields[i+1].(string)
			if !ok {
				return d, errors.New("invalid event data")
			}
			if err := json.Unmarshal([]byte(raw), &d.Event); err != nil {
				return d, err
			}
			if err := d.Event.Validate(); err != nil {
				return d, err
			}
			if d.Event.Scope.BotID != b.botID {
				return d, errors.New("cross-bot event")
			}
			return d, nil
		}
	}
	return d, errors.New("missing event data")
}

func (b *Bus) Ack(ctx context.Context, id string) error {
	_, err := b.command(ctx, "XACK", b.stream, group, id)
	return err
}
func (b *Bus) Ping(ctx context.Context) error { _, err := b.command(ctx, "PING"); return err }

func (b *Bus) command(ctx context.Context, args ...string) (any, error) {
	port := b.endpoint.Port()
	if port == "" {
		port = "6379"
	}
	address := net.JoinHostPort(b.endpoint.Hostname(), port)
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	var conn net.Conn
	var err error
	if b.endpoint.Scheme == "rediss" {
		td := tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: b.endpoint.Hostname(), MinVersion: tls.VersionTLS12}}
		conn, err = td.DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return nil, fmt.Errorf("Redis connection failed: %w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	r := bufio.NewReader(conn)
	if b.endpoint.User != nil {
		password, hasPassword := b.endpoint.User.Password()
		auth := []string{"AUTH", password}
		if user := b.endpoint.User.Username(); user != "" {
			if !hasPassword {
				auth = []string{"AUTH", user}
			} else {
				auth = []string{"AUTH", user, password}
			}
		}
		if _, err = exchange(conn, r, auth); err != nil {
			return nil, err
		}
	}
	if db := strings.Trim(b.endpoint.Path, "/"); db != "" && db != "0" {
		if _, err = exchange(conn, r, []string{"SELECT", db}); err != nil {
			return nil, err
		}
	}
	return exchange(conn, r, args)
}

func exchange(w io.Writer, r *bufio.Reader, args []string) (any, error) {
	var out strings.Builder
	fmt.Fprintf(&out, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(&out, "$%d\r\n%s\r\n", len(arg), arg)
	}
	if _, err := io.WriteString(w, out.String()); err != nil {
		return nil, err
	}
	return readRESP(r, 0)
}
func readRESP(r *bufio.Reader, depth int) (any, error) {
	if depth > 16 {
		return nil, errors.New("Redis reply nesting limit")
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 3 || !strings.HasSuffix(line, "\r\n") {
		return nil, errors.New("invalid Redis response")
	}
	body := line[1 : len(line)-2]
	switch line[0] {
	case '+':
		return body, nil
	case '-':
		return nil, errors.New(body)
	case ':':
		return strconv.ParseInt(body, 10, 64)
	case '$', '*':
		n, err := strconv.Atoi(body)
		if err != nil || n < -1 || n > 8*1024*1024 {
			return nil, errors.New("Redis reply size limit")
		}
		if n == -1 {
			return nil, nil
		}
		if line[0] == '$' {
			buf := make([]byte, n+2)
			if _, err = io.ReadFull(r, buf); err != nil {
				return nil, err
			}
			if string(buf[n:]) != "\r\n" {
				return nil, errors.New("invalid Redis bulk response")
			}
			return string(buf[:n]), nil
		}
		if n > 10000 {
			return nil, errors.New("Redis array size limit")
		}
		a := make([]any, n)
		for i := range a {
			a[i], err = readRESP(r, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return a, nil
	default:
		return nil, errors.New("unsupported Redis response")
	}
}
