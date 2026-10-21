package qq

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/supertx/qq-tavern/internal/domain"
)

const maxDocumentBytes = 8 << 20

func (g *Gateway) handleDocument(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+g.cfg.InternalToken)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var request mediaRequest
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var extra any
	if d.Decode(&request) != nil || d.Decode(&extra) != io.EOF || request.Event.Validate() != nil || request.Event.Scope.BotID != g.botID {
		http.Error(w, "invalid document request", 400)
		return
	}
	e := request.Event
	if e.ObserveOnly || e.CreatedAt.IsZero() || !time.Now().Before(e.Deadline) || !time.Now().Before(e.CreatedAt.Add(285*time.Second)) {
		http.Error(w, "expired or observed event", 410)
		return
	}
	attachments := append([]domain.FileAttachment{}, e.Files...)
	for _, q := range e.Quotes {
		attachments = append(attachments, q.Files...)
	}
	found := false
	for _, f := range attachments {
		if f.URL == request.URL {
			if f.Size > maxDocumentBytes {
				http.Error(w, "document exceeds 8 MiB", 413)
				return
			}
			found = true
		}
	}
	if !found {
		http.Error(w, "document is not part of this turn", 400)
		return
	}
	ctx, cancel := context.WithDeadline(r.Context(), e.Deadline)
	defer cancel()
	data, _, err := g.fetchAttachment(ctx, request.URL, maxDocumentBytes)
	if err != nil {
		http.Error(w, "文件下载失败、超过 8 MiB 或链接已过期", 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

func (s *RemoteSender) FetchDocument(ctx context.Context, e domain.Event, raw string) ([]byte, error) {
	data, err := json.Marshal(mediaRequest{Event: e, URL: raw})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.baseURL+"/document", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return nil, errors.New("文件下载网关不可用")
	}
	defer res.Body.Close()
	data, err = io.ReadAll(io.LimitReader(res.Body, maxDocumentBytes+1))
	if err != nil || res.StatusCode != 200 || len(data) > maxDocumentBytes {
		return nil, errors.New("文件下载失败、超过 8 MiB 或链接已过期，请重新发送或改用管理 API")
	}
	return data, nil
}
