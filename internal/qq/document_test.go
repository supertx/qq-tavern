package qq

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/supertx/qq-tavern/internal/domain"
)

func TestJSONAttachmentIsNotImageAndQuotedFilesSurvive(t *testing.T) {
	raw := []byte(`{"op":0,"t":"GROUP_AT_MESSAGE_CREATE","d":{"id":"file","group_openid":"g","author":{"member_openid":"u"},"content":"<@123> /导入","attachments":[{"filename":"世界.JSON","content_type":"application/octet-stream","url":"//example.com/settings","size":120}],"msg_elements":[{"message_type":103,"attachments":[{"content_type":"application/json","url":"https://example.com/quoted","filename":"q.json"}]}]}}`)
	e, err := ParseEvent(raw, "bot", time.Now(), false)
	if err != nil || len(e.Files) != 1 || len(e.Images) != 0 || len(e.Quotes) != 1 || len(e.Quotes[0].Files) != 1 {
		t.Fatal(e, err)
	}
	if e.Files[0].URL != "https://example.com/settings" || e.Files[0].Size != 120 {
		t.Fatal(e.Files)
	}
}

func TestDocumentDownloadBoundaryAndLimits(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { tokenResponse(w) }))
	defer api.Close()
	g := newTestGateway(t, api.URL, "", nil)
	calls := 0
	g.mediaClient = &http.Client{Transport: mediaTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host == "gchat.qpic.cn" && r.Header.Get("Authorization") != "QQBot test-access" {
			t.Error("missing QQ CDN credential")
		}
		if r.URL.Host == "example.com" && r.Header.Get("Authorization") != "" {
			t.Error("credential leaked to third party")
		}
		value, status := `{"format":"qq-tavern.bundle"}`, 200
		if r.URL.Path == "/large" {
			value = strings.Repeat("x", maxDocumentBytes+1)
		}
		if r.URL.Path == "/redirect" {
			status = 302
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://example.com/leak"}}, Body: io.NopCloser(strings.NewReader(value)), Request: r}, nil
	})}
	server := httptest.NewServer(g.Handler())
	defer server.Close()
	sender := NewRemoteSender(server.URL, internalTestToken)
	e := testRequest().Event
	for _, raw := range []string{"https://gchat.qpic.cn/doc", "https://example.com/doc"} {
		e.Files = []domain.FileAttachment{{URL: raw, Filename: "bundle.json"}}
		if data, err := sender.FetchDocument(context.Background(), e, raw); err != nil || !strings.Contains(string(data), "bundle") {
			t.Fatal(string(data), err)
		}
	}
	before := calls
	if _, err := sender.FetchDocument(context.Background(), e, "https://example.com/other"); err == nil || calls != before {
		t.Fatal("accepted URL outside event")
	}
	for _, raw := range []string{"https://gchat.qpic.cn/redirect", "https://example.com/large", "http://127.0.0.1/doc"} {
		e.Files = []domain.FileAttachment{{URL: raw, Filename: "bundle.json"}}
		if _, err := sender.FetchDocument(context.Background(), e, raw); err == nil {
			t.Fatal("accepted redirect, oversized document or internal URL", raw)
		}
	}
	e.Files = nil
	e.Quotes = []domain.Quote{{Files: []domain.FileAttachment{{URL: "https://example.com/quoted", Filename: "q.json"}}}}
	if _, err := sender.FetchDocument(context.Background(), e, e.Quotes[0].Files[0].URL); err != nil {
		t.Fatal("quoted attachment rejected", err)
	}
	e.ObserveOnly = true
	before = calls
	if _, err := sender.FetchDocument(context.Background(), e, e.Quotes[0].Files[0].URL); err == nil || calls != before {
		t.Fatal("observation downloaded document")
	}
}
