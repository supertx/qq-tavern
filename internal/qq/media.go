package qq

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/media"
	"github.com/supertx/qq-tavern/internal/netguard"
)

type mediaRequest struct {
	Event domain.Event `json:"event"`
	URL   string       `json:"url"`
}

func (g *Gateway) handleMedia(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+g.cfg.InternalToken)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var request mediaRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var extra any
	if dec.Decode(&request) != nil || dec.Decode(&extra) != io.EOF || request.Event.Validate() != nil || request.Event.Scope.BotID != g.botID {
		http.Error(w, "invalid media request", 400)
		return
	}
	if request.Event.CreatedAt.IsZero() || !time.Now().Before(request.Event.Deadline) || !time.Now().Before(request.Event.CreatedAt.Add(285*time.Second)) {
		http.Error(w, "expired media request", 410)
		return
	}
	found := false
	for _, raw := range request.Event.Images {
		found = found || raw == request.URL
	}
	for _, q := range request.Event.Quotes {
		for _, raw := range q.Images {
			found = found || raw == request.URL
		}
	}
	if !found {
		http.Error(w, "image is not part of this turn", 400)
		return
	}
	ctx, cancel := context.WithDeadline(r.Context(), request.Event.Deadline)
	defer cancel()
	data, mime, err := g.fetchMedia(ctx, request.URL)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Write(data)
}

func (g *Gateway) fetchMedia(ctx context.Context, raw string) ([]byte, string, error) {
	return g.fetchAttachment(ctx, raw, media.MaxInputBytes)
}

func (g *Gateway) fetchAttachment(ctx context.Context, raw string, maxBytes int64) ([]byte, string, error) {
	if err := netguard.Validate(raw); err != nil {
		return nil, "", err
	}
	// QQ credentials never leave the HTTPS QQ CDN allowlist, even on redirects.
	authenticated := IsQQMediaURL(raw) && strings.HasPrefix(raw, "https://")
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("User-Agent", "qq-traven/0.2")
		client := *g.mediaClient
		if authenticated {
			token, err := g.accessToken(ctx)
			if err != nil {
				return nil, "", err
			}
			req.Header.Set("Authorization", "QQBot "+token)
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, "", errors.New("图片下载失败")
		}
		data, readErr := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
		res.Body.Close()
		if res.StatusCode == 401 && authenticated && attempt == 0 {
			g.invalidateToken()
			continue
		}
		if res.StatusCode != 200 || readErr != nil {
			return nil, "", errors.New("图片下载失败或链接已过期")
		}
		if int64(len(data)) > maxBytes {
			return nil, "", errors.New("附件超过下载大小限制")
		}
		return data, res.Header.Get("Content-Type"), nil
	}
	return nil, "", errors.New("图片鉴权失败")
}

func (s *RemoteSender) FetchMedia(ctx context.Context, e domain.Event, raw string) ([]byte, string, error) {
	data, err := json.Marshal(mediaRequest{Event: e, URL: raw})
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.baseURL+"/media", bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return nil, "", errors.New("图片下载网关不可用")
	}
	defer res.Body.Close()
	data, err = io.ReadAll(io.LimitReader(res.Body, media.MaxInputBytes+1))
	if res.StatusCode != 200 || err != nil {
		return nil, "", errors.New("图片下载失败，请重新发送原图后重试")
	}
	if len(data) > media.MaxInputBytes {
		return nil, "", errors.New("图片超过 10 MiB")
	}
	return data, res.Header.Get("Content-Type"), nil
}
