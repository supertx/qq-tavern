package worker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/supertx/qq-tavern/internal/domain"
	"github.com/supertx/qq-tavern/internal/model"
)

type uncertainSender struct{ requests []domain.SendRequest }

func (s *uncertainSender) Send(_ context.Context, request domain.SendRequest) error {
	s.requests = append(s.requests, request)
	return errors.New("connection lost after QQ might have accepted media")
}

func TestUncertainMediaDoesNotAppendGenericFailure(t *testing.T) {
	calls := 0
	svc, _, _ := workerFixture(t, generateFunc(func(context.Context, model.Request) (model.Response, error) {
		calls++
		if calls == 1 {
			return model.Response{Calls: []model.Call{{ID: "image", Name: "generate_image", Arguments: `{"prompt":"a landscape"}`}}}, nil
		}
		return model.Response{}, errors.New("provider unavailable after tool")
	}))
	sender := &uncertainSender{}
	svc.sender = sender
	svc.tools.sender = sender
	svc.tools.cfg.ImageBase = "https://images.example/v1"
	svc.tools.cfg.ImageKey = "dummy"
	svc.tools.cfg.ImageModel = "dummy-image"
	svc.tools.client = &http.Client{Transport: toolsTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aW1hZ2U="}]}`))}, nil
	})}
	if err := svc.Handle(context.Background(), workerEvent("uncertain", "group", "member", "画一幅风景")); err == nil {
		t.Fatal("expected provider failure")
	}
	if len(sender.requests) != 1 || sender.requests[0].Media == nil {
		t.Fatal("an uncertain media request triggered an additional failure reply", sender.requests)
	}
}
