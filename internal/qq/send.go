package qq

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/supertx/qq-tavern/internal/domain"
)

var ErrExpired = errors.New("QQ reply deadline expired")
var ErrAmbiguous = errors.New("previous QQ send outcome is uncertain; automatic replay suppressed")

// SplitText keeps Unicode characters intact and reports truncation in the final chunk.
func SplitText(text string, maxChunks int) []string {
	if maxChunks <= 0 {
		return nil
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) == 0 {
		return nil
	}
	if len(runes) > 1800*maxChunks {
		suffix := []rune("\n[回复过长，余下内容已省略]")
		runes = append(runes[:1800*maxChunks-len(suffix)], suffix...)
	}
	var result []string
	for len(runes) > 0 {
		n := min(1800, len(runes))
		result = append(result, string(runes[:n]))
		runes = runes[n:]
	}
	return result
}

type sendPart struct {
	text      string
	media     *domain.Media
	key, hash string
	seq       int
	done      bool
}

func (g *Gateway) Send(ctx context.Context, request domain.SendRequest) error {
	if err := request.Event.Validate(); err != nil {
		return err
	}
	if request.Event.Scope.BotID != g.botID {
		return errors.New("send request belongs to another bot")
	}
	if request.Key == "" || len(request.Key) > 256 {
		return errors.New("send idempotency key is required (max 256 bytes)")
	}
	if request.Event.ObserveOnly {
		return errors.New("observed messages cannot receive automatic replies")
	}
	deadline := request.Event.Deadline
	if request.Event.CreatedAt.IsZero() {
		return errors.New("event creation time is required")
	}
	if cap := request.Event.CreatedAt.Add(285 * time.Second); cap.Before(deadline) {
		deadline = cap
	}
	if !time.Now().Before(deadline) {
		return ErrExpired
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	maxReplies := 5
	if request.Event.Scope.Kind == "private" {
		maxReplies = 4
	}
	textSlots := maxReplies
	if request.Media != nil {
		textSlots--
		if err := validateMedia(*request.Media); err != nil {
			return err
		}
	}
	var parts []sendPart
	if request.Media != nil {
		parts = append(parts, sendPart{media: request.Media})
	}
	for _, chunk := range SplitText(request.Text, textSlots) {
		parts = append(parts, sendPart{text: chunk})
	}
	if len(parts) == 0 {
		return errors.New("empty reply")
	}
	eventKey := domain.Key(request.Event.Scope.Key(), request.Event.MessageID)
	for i := range parts {
		parts[i].key = domain.Key(eventKey, request.Key, strconv.Itoa(i))
		raw, _ := json.Marshal(struct {
			Text  string
			Media *domain.Media
		}{parts[i].text, parts[i].media})
		parts[i].hash = domain.Key(string(raw))
	}
	// Serialize reservation and delivery; SQLite receipts survive process failure.
	g.sendMu.Lock()
	defer g.sendMu.Unlock()
	if err := g.reserve(ctx, eventKey, parts, maxReplies); err != nil {
		return err
	}
	path := "/v2/groups/"
	if request.Event.Scope.Kind == "private" {
		path = "/v2/users/"
	}
	path += url.PathEscape(request.Event.Scope.ID)
	for _, part := range parts {
		if part.done {
			continue
		}
		if ctx.Err() != nil {
			return ErrExpired
		}
		body := map[string]any{"msg_type": 0, "content": part.text, "msg_id": request.Event.MessageID, "msg_seq": part.seq}
		if part.media != nil {
			info, err := g.upload(ctx, path, *part.media)
			if err != nil {
				return err
			}
			body["msg_type"] = 7
			body["media"] = map[string]string{"file_info": info}
			delete(body, "content")
		}
		if err := g.api(ctx, http.MethodPost, path+"/messages", body, nil); err != nil {
			return err
		}
		// A crash or deadline here leaves a claim, never an automatic resend.
		if _, err := g.db.ExecContext(ctx, "UPDATE send_receipts SET state='sent' WHERE request_key=?", part.key); err != nil {
			return fmt.Errorf("QQ accepted reply but receipt commit failed: %w", err)
		}
	}
	return nil
}

func (g *Gateway) reserve(ctx context.Context, eventKey string, parts []sendPart, limit int) error {
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var next int
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM send_receipts WHERE event_key=?", eventKey).Scan(&next); err != nil {
		return err
	}
	for i := range parts {
		var hash, state string
		err = tx.QueryRowContext(ctx, "SELECT seq,payload_hash,state FROM send_receipts WHERE request_key=?", parts[i].key).Scan(&parts[i].seq, &hash, &state)
		if err == nil {
			if hash != parts[i].hash {
				return errors.New("send idempotency key reused with different content")
			}
			if state != "sent" {
				return ErrAmbiguous
			}
			parts[i].done = true
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		next++
		if next > limit {
			return errors.New("QQ passive reply count exhausted")
		}
		parts[i].seq = next
		if _, err = tx.ExecContext(ctx, "INSERT INTO send_receipts(request_key,event_key,seq,payload_hash,state,created) VALUES(?,?,?,?,'claimed',?)", parts[i].key, eventKey, next, parts[i].hash, time.Now().Unix()); err != nil {
			return err
		}
	}
	// Expired source messages cannot be resent, so old receipts can be discarded safely.
	if _, err = tx.ExecContext(ctx, "DELETE FROM send_receipts WHERE created < ?", time.Now().Add(-7*24*time.Hour).Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func validateMedia(media domain.Media) error {
	if media.Kind != "image" && media.Kind != "audio" {
		return errors.New("media kind must be image or audio")
	}
	if (media.URL == "") == (len(media.Data) == 0) {
		return errors.New("media requires exactly one of URL or data")
	}
	// shortcut: direct QQ binary uploads are limited to 5 MiB; add multipart uploads when larger generated files are required.
	if len(media.Data) > 5<<20 {
		return errors.New("QQ direct media upload exceeds 5 MiB; use a public media URL")
	}
	if media.URL != "" {
		u, err := url.Parse(media.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return errors.New("media URL must be HTTP(S)")
		}
	}
	return nil
}
func (g *Gateway) upload(ctx context.Context, path string, media domain.Media) (string, error) {
	fileType := 1
	if media.Kind == "audio" {
		fileType = 3
	}
	body := map[string]any{"file_type": fileType, "srv_send_msg": false}
	if media.URL != "" {
		body["url"] = media.URL
	} else {
		body["file_data"] = base64.StdEncoding.EncodeToString(media.Data)
		extension := ".png"
		if media.Kind == "audio" {
			extension = ".wav"
		}
		if extensions, err := mime.ExtensionsByType(media.MIME); err == nil && len(extensions) > 0 {
			extension = extensions[0]
		}
		body["file_name"] = "reply" + extension
	}
	var result struct {
		Info string `json:"file_info"`
	}
	if err := g.api(ctx, http.MethodPost, path+"/files", body, &result); err != nil {
		return "", err
	}
	if result.Info == "" {
		return "", errors.New("QQ media upload returned no file_info")
	}
	return result.Info, nil
}

type RemoteSender struct {
	baseURL, token string
	client         *http.Client
}

func NewRemoteSender(baseURL, token string) *RemoteSender {
	return &RemoteSender{baseURL: strings.TrimRight(baseURL, "/"), token: token, client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (s *RemoteSender) Send(ctx context.Context, request domain.SendRequest) error {
	if request.Event.Deadline.IsZero() || !time.Now().Before(request.Event.Deadline) {
		return ErrExpired
	}
	ctx, cancel := context.WithDeadline(ctx, request.Event.Deadline)
	defer cancel()
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/send", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(req)
	if err != nil {
		return errors.New("gateway send transport failed; delivery may be uncertain")
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		if response.StatusCode == http.StatusConflict {
			return ErrAmbiguous
		}
		if response.StatusCode == http.StatusGone {
			return ErrExpired
		}
		return fmt.Errorf("gateway send HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
