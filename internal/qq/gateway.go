package qq

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/supertx/qq-tavern/internal/domain"
	_ "modernc.org/sqlite"
)

const DefaultAPIBase = "https://api.bot.qq.com"
const DefaultTokenURL = "https://api.bot.qq.com/app/getAppAccessToken"

type Publisher interface {
	Publish(context.Context, domain.Event) error
}
type Config struct {
	AppID, Secret, APIBase, TokenURL, InternalToken, DBPath string
	ObserveGroupMessages                                    bool
}
type Gateway struct {
	cfg          Config
	publisher    Publisher
	client       *http.Client
	db           *sql.DB
	botID        string
	tokenMu      sync.Mutex
	token        string
	tokenExpires time.Time
	sendMu       sync.Mutex
	online       atomic.Bool
}

func NewGateway(cfg Config, publisher Publisher) (*Gateway, error) {
	if cfg.AppID == "" || cfg.Secret == "" {
		return nil, errors.New("QQ_APP_ID and QQ_APP_SECRET are required")
	}
	if len(cfg.InternalToken) < 24 {
		return nil, errors.New("INTERNAL_TOKEN must contain at least 24 characters")
	}
	if cfg.DBPath == "" {
		return nil, errors.New("gateway database path is required")
	}
	if cfg.APIBase == "" {
		cfg.APIBase = DefaultAPIBase
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = DefaultTokenURL
	}
	for _, endpoint := range []string{cfg.APIBase, cfg.TokenURL} {
		u, e := url.Parse(endpoint)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return nil, errors.New("invalid QQ endpoint URL")
		}
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS send_receipts (request_key TEXT PRIMARY KEY,event_key TEXT NOT NULL,seq INTEGER NOT NULL,payload_hash TEXT NOT NULL,state TEXT NOT NULL,created INTEGER NOT NULL,UNIQUE(event_key,seq));`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Gateway{cfg: cfg, publisher: publisher, client: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, db: db, botID: domain.Key(cfg.AppID)}, nil
}
func (g *Gateway) Close() error { return g.db.Close() }

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if !g.online.Load() || g.db.PingContext(ctx) != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		if p, ok := g.publisher.(interface{ Ping(context.Context) error }); ok && p.Ping(ctx) != nil {
			http.Error(w, "queue unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true, "qq_connected": g.online.Load()})
	})
	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+g.cfg.InternalToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 30<<20)
		var request domain.SendRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&request); err != nil {
			http.Error(w, "invalid send request", http.StatusBadRequest)
			return
		}
		var trailing any
		if dec.Decode(&trailing) != io.EOF {
			http.Error(w, "invalid trailing data", http.StatusBadRequest)
			return
		}
		if err := g.Send(r.Context(), request); err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, ErrExpired) {
				status = http.StatusGone
			}
			if errors.Is(err, ErrAmbiguous) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func (g *Gateway) accessToken(ctx context.Context) (string, error) {
	g.tokenMu.Lock()
	defer g.tokenMu.Unlock()
	if g.token != "" && time.Now().Before(g.tokenExpires.Add(-30*time.Second)) {
		return g.token, nil
	}
	data, _ := json.Marshal(map[string]string{"appId": g.cfg.AppID, "clientSecret": g.cfg.Secret})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, g.cfg.TokenURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := g.client.Do(request)
	if err != nil {
		return "", errors.New("QQ access token request failed")
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return "", fmt.Errorf("QQ access token HTTP %d", response.StatusCode)
	}
	var value struct {
		Token   string          `json:"access_token"`
		Expires json.RawMessage `json:"expires_in"`
		Code    int             `json:"code"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&value); err != nil {
		return "", errors.New("invalid QQ access token response")
	}
	if value.Code != 0 || value.Token == "" {
		return "", fmt.Errorf("QQ access token rejected (code %d)", value.Code)
	}
	seconds, err := strconv.Atoi(strings.Trim(string(value.Expires), `"`))
	if err != nil || seconds <= 0 {
		return "", errors.New("invalid QQ access token expiry")
	}
	g.token = value.Token
	g.tokenExpires = time.Now().Add(time.Duration(seconds) * time.Second)
	return g.token, nil
}

func (g *Gateway) api(ctx context.Context, method, path string, body any, target any) error {
	token, err := g.accessToken(ctx)
	if err != nil {
		return err
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(g.cfg.APIBase, "/")+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("X-Union-Appid", g.cfg.AppID)
	req.Header.Set("Content-Type", "application/json")
	response, err := g.client.Do(req)
	if err != nil {
		return errors.New("QQ API transport failed; delivery may be uncertain")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return errors.New("QQ API response incomplete")
	}
	var failure struct {
		Code int `json:"code"`
	}
	_ = json.Unmarshal(raw, &failure)
	if response.StatusCode/100 != 2 || failure.Code != 0 {
		if response.StatusCode == 401 {
			g.tokenMu.Lock()
			g.tokenExpires = time.Time{}
			g.tokenMu.Unlock()
		}
		return fmt.Errorf("QQ API rejected: HTTP %d code %d", response.StatusCode, failure.Code)
	}
	if target != nil && len(raw) > 0 {
		if err = json.Unmarshal(raw, target); err != nil {
			return errors.New("invalid QQ API response")
		}
	}
	return nil
}

// Run reconnects using the last acknowledged dispatch sequence. Event publication
// must finish before advancing the resume cursor so Redis failures do not lose events.
func (g *Gateway) Run(ctx context.Context) error {
	if g.publisher == nil {
		return errors.New("event publisher is required")
	}
	var session string
	var sequence int64
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := g.connect(ctx, &session, &sequence)
		g.online.Store(false)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("QQ websocket disconnected", "error", err, "retry_seconds", backoff.Seconds())
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
	return ctx.Err()
}

func (g *Gateway) connect(ctx context.Context, session *string, sequence *int64) error {
	var endpoint struct {
		URL string `json:"url"`
	}
	if err := g.api(ctx, http.MethodGet, "/gateway/bot", nil, &endpoint); err != nil {
		return err
	}
	if endpoint.URL == "" {
		return errors.New("QQ gateway returned no websocket URL")
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, endpoint.URL, nil)
	if err != nil {
		return errors.New("QQ websocket dial failed")
	}
	defer conn.Close()
	conn.SetReadLimit(2 << 20)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	var hello payload
	if err = conn.ReadJSON(&hello); err != nil {
		return err
	}
	if hello.Op != 10 {
		return errors.New("QQ websocket did not send Hello")
	}
	var h struct {
		Interval int64 `json:"heartbeat_interval"`
	}
	if err = json.Unmarshal(hello.Data, &h); err != nil || h.Interval < 10 || h.Interval > 300000 {
		return errors.New("invalid QQ heartbeat interval")
	}
	token, err := g.accessToken(ctx)
	if err != nil {
		return err
	}
	var identify any = map[string]any{"op": 2, "d": map[string]any{"token": "QQBot " + token, "intents": 1 << 25, "shard": []int{0, 1}}}
	if *session != "" {
		identify = map[string]any{"op": 6, "d": map[string]any{"token": "QQBot " + token, "session_id": *session, "seq": *sequence}}
	}
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err = conn.WriteJSON(identify); err != nil {
		return err
	}
	packets := make(chan payload)
	readErrors := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			_ = conn.SetReadDeadline(time.Now().Add(time.Duration(h.Interval) * time.Millisecond * 3))
			var p payload
			if e := conn.ReadJSON(&p); e != nil {
				readErrors <- e
				return
			}
			select {
			case packets <- p:
			case <-done:
				return
			}
		}
	}()
	ticker := time.NewTicker(time.Duration(h.Interval) * time.Millisecond)
	defer ticker.Stop()
	waitingACK := false
	heartbeat := func() error {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		var seq any
		if *sequence > 0 {
			seq = *sequence
		}
		return conn.WriteJSON(map[string]any{"op": 1, "d": seq})
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err = <-readErrors:
			return err
		case <-ticker.C:
			if waitingACK {
				return errors.New("QQ heartbeat acknowledgement timeout")
			}
			if err = heartbeat(); err != nil {
				return err
			}
			waitingACK = true
		case p := <-packets:
			switch p.Op {
			case 11:
				waitingACK = false
			case 1:
				if err = heartbeat(); err != nil {
					return err
				}
			case 7:
				return errors.New("QQ requested reconnect")
			case 9:
				*session = ""
				*sequence = 0
				return errors.New("QQ session invalid")
			case 0:
				if p.Type == "READY" {
					var ready struct {
						Session string `json:"session_id"`
					}
					if err = json.Unmarshal(p.Data, &ready); err != nil {
						return err
					}
					*session = ready.Session
					g.online.Store(true)
				} else if p.Type == "RESUMED" {
					g.online.Store(true)
				} else {
					raw, _ := json.Marshal(p)
					event, e := ParseEvent(raw, g.botID, time.Now(), g.cfg.ObserveGroupMessages)
					if e != nil {
						slog.Warn("discarded malformed QQ event", "type", p.Type)
					} else if event != nil {
						if e = g.publisher.Publish(ctx, *event); e != nil {
							return errors.New("QQ event could not be persisted")
						}
					}
				}
				if p.Sequence != nil {
					*sequence = *p.Sequence
				}
			}
		}
	}
}
