package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/supertx/qq-tavern/internal/config"
	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/model"
)

func TestGroupObservationOnlyEntersRecentTool(t *testing.T) {
	calls := 0
	svc, store, sender := workerFixture(t, generateFunc(func(context.Context, model.Request) (model.Response, error) {
		calls++
		return model.Response{Text: "ok"}, nil
	}))
	e := workerEvent("observed", "group", "alice", strings.Repeat("\"secret\"", 600))
	e.Images = []string{"https://gchat.qpic.cn/signed-secret"}
	e.ObserveOnly = true
	handle(t, svc, e)
	handle(t, svc, e)
	if calls != 0 || len(sender.requests) != 0 {
		t.Fatal("observation invoked model/send")
	}
	history, err := store.History(context.Background(), e.Scope, 20)
	if err != nil || len(history) != 0 {
		t.Fatal("observation polluted conversation", err)
	}
	roles, err := store.DueRoleSummaries(context.Background(), e.CreatedAt.Add(1000000000000))
	if err != nil || len(roles) != 0 {
		t.Fatal("observation started summary", err)
	}
	current := workerEvent("query", "group", "bob", "recent")
	view, err := store.BindMessage(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	session := svc.tools.Session(current)
	session.store = view
	result := session.Execute(context.Background(), model.Call{Name: "recent_messages", Arguments: `{"limit":20}`})
	if !strings.Contains(result, "secret") || strings.Contains(result, "signed-secret") || strings.Contains(result, "alice") || utf8.RuneCountInString(result) > 6000 {
		t.Fatal(result)
	}
	for _, args := range []string{`{"limit":0}`, `{"limit":21}`, `{"group_id":"other"}`} {
		if toolResultError(t, session.Execute(context.Background(), model.Call{Name: "recent_messages", Arguments: args})) == "" {
			t.Fatal(args)
		}
	}
	current.Scope.Kind = "private"
	current.Scope.ID = current.UserID
	if toolResultError(t, svc.tools.Session(current).Execute(context.Background(), model.Call{Name: "recent_messages", Arguments: `{}`})) == "" {
		t.Fatal("private recent exposed")
	}
}

func TestEditQuotedImageResizesAndSelectsModel(t *testing.T) {
	var input bytes.Buffer
	png.Encode(&input, image.NewNRGBA(image.Rect(0, 0, 4096, 1024)))
	sender := &toolsSender{}
	tools := NewTools(config.Config{ImageBase: "https://images.example/v1", ImageKey: "key", ImageModel: "generation", ImageEditModel: "editing"}, nil, sender)
	tools.public = &http.Client{Transport: toolsTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://gchat.qpic.cn/quoted" {
			t.Error(r.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/octet-stream"}}, Body: io.NopCloser(bytes.NewReader(input.Bytes())), Request: r}, nil
	})}
	calls := 0
	tools.client = &http.Client{Transport: toolsTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/v1/images/edits" {
			t.Fatal(r.URL)
		}
		if err := r.ParseMultipartForm(12 << 20); err != nil {
			t.Fatal(err)
		}
		defer r.MultipartForm.RemoveAll()
		if r.FormValue("model") != "editing" || r.FormValue("n") != "1" {
			t.Error("edit model/format")
		}
		file, header, err := r.FormFile("image")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		cfg, _, err := image.DecodeConfig(file)
		if err != nil || cfg.Width != 2048 || cfg.Height != 512 || header.Header.Get("Content-Type") != "image/png" {
			t.Fatal(cfg, err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(imageFixture(t)) + `"}]}`)), Request: r}, nil
	})}
	e := toolsEvent("g", "e")
	e.Images = []string{"https://example.com/not-selected"}
	e.Quotes = []domain.Quote{{Images: []string{"https://gchat.qpic.cn/quoted"}}}
	session := tools.Session(e)
	result := session.Execute(context.Background(), model.Call{ID: "edit", Name: "edit_image", Arguments: `{"prompt":"change color","image_index":1}`})
	if toolResultError(t, result) != "" || calls != 1 || len(sender.requests) != 1 {
		t.Fatal(result, calls)
	}
	if result != session.Execute(context.Background(), model.Call{ID: "duplicate", Name: "edit_image", Arguments: `{"image_index":1,"prompt":"change color"}`}) || calls != 1 {
		t.Fatal("edit duplicated")
	}
}

func TestVisionFetchesQuotedInputAndDoesNotReplayBytes(t *testing.T) {
	var requests []model.Request
	svc, store, _ := workerFixture(t, generateFunc(func(_ context.Context, r model.Request) (model.Response, error) {
		requests = append(requests, r)
		return model.Response{Text: "ok"}, nil
	}))
	svc.tools.public = &http.Client{Transport: toolsTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(imageFixture(t))), Request: r}, nil
	})}
	e := workerEvent("image", "g", "u", "look")
	e.Quotes = []domain.Quote{{Images: []string{"https://gchat.qpic.cn/quoted"}}}
	handle(t, svc, e)
	handle(t, svc, workerEvent("next", "g", "u", "next"))
	if len(requests[0].Messages[0].Images) != 1 || !strings.HasPrefix(requests[0].Messages[0].Images[0], "data:image/png;base64,") {
		t.Fatal("image not resolved")
	}
	for _, m := range requests[1].Messages {
		if len(m.Images) > 0 {
			t.Fatal("stale image replayed")
		}
	}
	history, err := store.History(context.Background(), e.Scope, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range history {
		if bytes.Contains(h.Payload, []byte("data:image/")) {
			t.Fatal("large inline image persisted")
		}
	}
}
