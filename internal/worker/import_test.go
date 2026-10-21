package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/model"
	"github.com/supertx/qq-tavern/internal/state"
)

type fileSender struct {
	*recordingSender
	data  []byte
	err   error
	calls int
}

func (s *fileSender) FetchDocument(_ context.Context, e domain.Event, raw string) ([]byte, error) {
	s.calls++
	return s.data, s.err
}

func TestQQFileImportPrivateAndGroupQuote(t *testing.T) {
	for _, group := range []bool{false, true} {
		t.Run(map[bool]string{false: "private", true: "group-quote"}[group], func(t *testing.T) {
			svc, store, recorder := workerFixture(t, generateFunc(func(context.Context, model.Request) (model.Response, error) {
				t.Fatal("file import invoked model")
				return model.Response{}, nil
			}))
			sender := &fileSender{recordingSender: recorder, data: []byte(`{"format":"qq-tavern.bundle","version":1,"roles":[{"name":"新角色","character":{"description":"文档角色"}}]}`)}
			svc.sender = sender
			e := workerEvent("file", "g", "alice", "")
			e.Files = []domain.FileAttachment{{URL: "https://example.com/bundle.json", Filename: "bundle.json"}}
			if !group {
				e.Scope.Kind, e.Scope.ID = "private", "alice"
			} else {
				// Observation records metadata, performs no download or reply.
				e.ObserveOnly = true
				e.MessageIndex = "file-index"
				handle(t, svc, e)
				if sender.calls != 0 || len(recorder.requests) != 0 {
					t.Fatal("observation imported a file")
				}
				e.ID, e.MessageID, e.Text = "import", "import", "/导入"
				e.ObserveOnly = false
				e.Files = nil
				e.ReferencedMessageIndex = "file-index"
				e.Quotes = []domain.Quote{{Text: "[引用消息正文未由 QQ 提供]"}}
			}
			handle(t, svc, e)
			handle(t, svc, e)
			if sender.calls != 1 || len(recorder.requests) != 1 || !strings.Contains(recorder.requests[0].Text, "尚未保存") {
				t.Fatal("missing preview or duplicate download", recorder.requests)
			}
			text := recorder.requests[0].Text
			id := strings.Fields(strings.Split(text, "/导入 确认 ")[1])[0]
			roles, err := store.Roles(context.Background(), e.Scope)
			if err != nil || len(roles) != 1 {
				t.Fatal("preview changed roles", roles, err)
			}
			e.ID, e.MessageID, e.Text = "confirm", "confirm", "/导入 确认 "+id
			e.Files, e.Quotes = nil, nil
			e.ReferencedMessageIndex = ""
			handle(t, svc, e)
			e.ID, e.MessageID = "confirm-again", "confirm-again"
			handle(t, svc, e)
			roles, err = store.Roles(context.Background(), e.Scope)
			if err != nil || len(roles) != 2 || !roles[0].Active || roles[1].Active {
				t.Fatal("import switched role or duplicated data", roles, err)
			}
		})
	}
}

func TestQQFileDeniedBeforeDownloadAndDownloadFailure(t *testing.T) {
	svc, store, recorder := workerFixture(t, generateFunc(func(context.Context, model.Request) (model.Response, error) {
		t.Fatal("file invoked model")
		return model.Response{}, nil
	}))
	sender := &fileSender{recordingSender: recorder, err: errors.New("文件下载失败")}
	svc.sender = sender
	_, err := store.SetPolicy(context.Background(), 1, map[state.CommandGroup]bool{state.RoleEdit: false, state.WorldEdit: false})
	if err != nil {
		t.Fatal(err)
	}
	e := workerEvent("denied", "", "alice", "")
	e.Scope.Kind, e.Scope.ID = "private", "alice"
	e.Files = []domain.FileAttachment{{URL: "https://example.com/bundle.json", Filename: "bundle.json"}}
	handle(t, svc, e)
	if sender.calls != 0 || !strings.Contains(recorder.requests[0].Text, "未开放") {
		t.Fatal("unauthorized download")
	}
	_, err = store.SetPolicy(context.Background(), 2, map[state.CommandGroup]bool{state.RoleEdit: true})
	if err != nil {
		t.Fatal(err)
	}
	e.ID, e.MessageID = "failed", "failed"
	handle(t, svc, e)
	if sender.calls != 1 || !strings.Contains(recorder.requests[1].Text, "下载失败") {
		t.Fatal("missing download failure")
	}
	e.ID, e.MessageID, e.Text, e.Files = "help", "help", "/导入", nil
	handle(t, svc, e)
	if !strings.Contains(recorder.requests[2].Text, "管理 API") {
		t.Fatal("missing fallback instructions")
	}
}

func TestManualSummaryRevocationBeforeSave(t *testing.T) {
	var store *state.Store
	svc, db, recorder := workerFixture(t, generateFunc(func(ctx context.Context, req model.Request) (model.Response, error) {
		p, err := store.Policy(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.SetPolicy(ctx, p.Version, map[state.CommandGroup]bool{state.ContextManage: false}); err != nil {
			t.Fatal(err)
		}
		return model.Response{Text: "不可保存的摘要"}, nil
	}))
	store = db
	e := workerEvent("history", "g", "alice", "old")
	view, err := store.BindActive(context.Background(), e.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if err = view.AddTurn(context.Background(), e, "m", []byte(`[{"role":"user","content":"old"}]`)); err != nil {
		t.Fatal(err)
	}
	e.ID, e.MessageID, e.Text = "summary", "summary", "/摘要 更新"
	handle(t, svc, e)
	summary, err := view.Summary(context.Background(), e.Scope)
	if err != nil || summary.Text != "" || !strings.Contains(recorder.requests[0].Text, "未开放") && !strings.Contains(recorder.requests[0].Text, "关闭") && !strings.Contains(recorder.requests[0].Text, "disabled") {
		t.Fatal("revoked summary persisted", summary, err, recorder.requests)
	}
	// Background summaries do not inherit the QQ command marker.
	if err = svc.summarizeRole(context.Background(), view, e.Scope); err != nil {
		t.Fatal(err)
	}
	summary, err = view.Summary(context.Background(), e.Scope)
	if err != nil || summary.Text == "" {
		t.Fatal("background summary disabled by QQ policy", summary, err)
	}
}
