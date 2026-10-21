package qq

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMarkdownRecognition(t *testing.T) {
	for _, text := range []string{"# 标题", "这是**重点**。", "- 项目", "1. 步骤", "> 引用", "[来源](https://example.com)", "~~删除~~", "*斜体*", "普通段落\n---"} {
		if !isMarkdown(text) {
			t.Errorf("missed %q", text)
		}
	}
	for _, text := range []string{"你好", "今天 3.14 元", "https://example.com", "file_name_test", "2 * 3 = 6", "\\*\\*只是字符\\*\\*"} {
		if isMarkdown(text) {
			t.Errorf("false positive %q", text)
		}
	}
}

func TestSendMarkdownUsesQQPayloadAndPreservesReplyIdentity(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Write([]byte(`{"access_token":"test","expires_in":7200}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, body)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	sender := newTestGateway(t, server.URL, "", nil)
	for _, kind := range []string{"group", "private"} {
		request := testRequest()
		request.Event.Scope.Kind = kind
		request.Event.Scope.ID = request.Event.UserID
		request.Event.MessageID = "trigger"
		text := "# 标题\n" + strings.Repeat("长正文。", 700)
		start := len(bodies)
		request.Text = text
		if err := sender.Send(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		var joined string
		seen := map[float64]bool{}
		for _, b := range bodies[start:] {
			if b["msg_type"] != float64(2) || b["content"] != nil || b["msg_id"] != "trigger" {
				t.Fatalf("bad payload %#v", b)
			}
			seq := b["msg_seq"].(float64)
			if seen[seq] {
				t.Fatal("duplicate sequence")
			}
			seen[seq] = true
			joined += b["markdown"].(map[string]any)["content"].(string)
		}
		if joined != text {
			t.Fatal("Markdown text changed")
		}
	}
	request := testRequest()
	request.Event.MessageID = "plain"
	request.Text = "普通文字"
	if err := sender.Send(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	last := bodies[len(bodies)-1]
	if last["msg_type"] != float64(0) || last["markdown"] != nil {
		t.Fatal("plain text format changed")
	}
}
