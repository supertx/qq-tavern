package qq

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQQEventVariants(t *testing.T) {
	raw := []byte(`{"op":0,"t":"GROUP_AT_MESSAGE_CREATE","d":{"id":"m","group_id":"g","author":{"user_openid":"u"},"content":"<@!123> /角色 列表","message_type":103,"msg_idx":"current","message_scene":{"ext":["ref_msg_idx=reference"]},"attachments":[{"src":"//gchat.qpic.cn/current","content_type":"application/octet-stream"}],"msg_elements":[{"author":{"member_openid":"quoted"},"content":"[附件1] 类型：图片 URL：https://multimedia.nt.qq.com.cn/image?token=secret 文件名：x.png"}]}}`)
	e, err := ParseEvent(raw, "bot", time.Now(), true)
	if err != nil || e == nil {
		t.Fatal(e, err)
	}
	if e.Text != "/角色 列表" || e.Scope.ID != "g" || e.UserID != "u" || e.MessageIndex != "current" || e.ReferencedMessageIndex != "reference" {
		t.Fatalf("wrong event %+v", e)
	}
	if len(e.Images) != 1 || len(e.Quotes) != 1 || len(e.Quotes[0].Images) != 1 || e.Quotes[0].AuthorID != "quoted" || strings.Contains(e.Quotes[0].Text, "secret") {
		t.Fatalf("missing/private quote %+v", e)
	}
	raw = []byte(strings.ReplaceAll(strings.ReplaceAll(string(raw), "GROUP_AT_MESSAGE_CREATE", "GROUP_MESSAGE_CREATE"), `"user_openid":"u"`, `"user_openid":"u","bot":true`))
	e, err = ParseEvent(raw, "bot", time.Now(), true)
	if err != nil || e == nil || !e.AuthorBot || !e.ObserveOnly {
		t.Fatal("bot observations lost", e, err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), "GROUP_MESSAGE_CREATE", "GROUP_AT_MESSAGE_CREATE"))
	if e, err = ParseEvent(raw, "bot", time.Now(), true); err != nil || e != nil {
		t.Fatal("bot mention caused reply", e, err)
	}
}

func TestQQCDNAllowlist(t *testing.T) {
	for _, raw := range []string{"https://multimedia.nt.qq.com.cn/x", "https://gchat.qpic.cn/x"} {
		if !IsQQMediaURL(raw) {
			t.Fatal(raw)
		}
	}
	for _, raw := range []string{"https://qpic.cn.evil.test/x", "https://evil@qpic.cn/x", "https://gchat.qpic.cn:9999/x", "http://127.0.0.1/x", "https://gchat.qpic.cn/x…"} {
		if IsQQMediaURL(raw) {
			t.Fatal(raw)
		}
	}
}

func TestQQExplicitRejectionRetriesSameMessage(t *testing.T) {
	for _, status := range []int{401, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			attempts, tokens := 0, 0
			var bodies []map[string]any
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					tokens++
					tokenResponse(w)
					return
				}
				attempts++
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				bodies = append(bodies, body)
				if attempts == 1 {
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(status)
					w.Write([]byte(`{"code":1}`))
					return
				}
				w.Write([]byte(`{}`))
			}))
			defer api.Close()
			g := newTestGateway(t, api.URL, "", nil)
			if err := g.Send(context.Background(), testRequest()); err != nil {
				t.Fatal(err)
			}
			if err := g.Send(context.Background(), testRequest()); err != nil {
				t.Fatal(err)
			}
			if attempts != 2 || bodies[0]["msg_seq"] != bodies[1]["msg_seq"] || bodies[0]["msg_id"] != bodies[1]["msg_id"] {
				t.Fatal(attempts, bodies)
			}
			if status == 401 && tokens != 2 {
				t.Fatal("token not refreshed", tokens)
			}
		})
	}
}

type mediaTransport func(*http.Request) (*http.Response, error)

func (f mediaTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestQQMediaAuthIsBoundToSourceAndCDN(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { tokenResponse(w) }))
	defer api.Close()
	g := newTestGateway(t, api.URL, "", nil)
	calls := 0
	g.mediaClient = &http.Client{Transport: mediaTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		auth := r.Header.Get("Authorization")
		if r.URL.Host == "gchat.qpic.cn" && auth != "QQBot test-access" {
			t.Error("missing CDN auth")
		}
		if r.URL.Host == "example.com" && auth != "" {
			t.Error("credentials leaked")
		}
		status := 200
		if r.URL.Path == "/redirect" {
			status = 302
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/octet-stream"}, "Location": {"https://example.com/leak"}}, Body: io.NopCloser(strings.NewReader("image")), Request: r}, nil
	})}
	server := httptest.NewServer(g.Handler())
	defer server.Close()
	sender := NewRemoteSender(server.URL, internalTestToken)
	e := testRequest().Event
	for _, raw := range []string{"https://gchat.qpic.cn/a", "https://example.com/a"} {
		e.Images = []string{raw}
		data, _, err := sender.FetchMedia(context.Background(), e, raw)
		if err != nil || string(data) != "image" {
			t.Fatal(err)
		}
	}
	before := calls
	if _, _, err := sender.FetchMedia(context.Background(), e, "https://gchat.qpic.cn/not-in-event"); err == nil || calls != before {
		t.Fatal("arbitrary image accepted")
	}
	e.Images = []string{"https://gchat.qpic.cn/redirect"}
	if _, _, err := sender.FetchMedia(context.Background(), e, e.Images[0]); err == nil || calls != before+1 {
		t.Fatal("credential-bearing redirect followed", calls)
	}
	e.Deadline = time.Now().Add(-time.Second)
	if _, _, err := sender.FetchMedia(context.Background(), e, e.Images[0]); err == nil {
		t.Fatal("expired download")
	}
}
