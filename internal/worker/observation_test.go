package worker

import (
	"context"
	"testing"

	"github.com/supertx/qq-tavern/internal/model"
)

func TestObservationAndReplyUseSourceMessageOnce(t *testing.T) {
	var request model.Request
	svc, store, _ := workerFixture(t, generateFunc(func(_ context.Context, r model.Request) (model.Response, error) {
		request = r
		return model.Response{Text: "reply"}, nil
	}))
	e := workerEvent("observe", "group", "member", "same-input")
	e.MessageID = "qq-source-message"
	e.ObserveOnly = true
	handle(t, svc, e)
	e.ID = "reply"
	e.ObserveOnly = false
	handle(t, svc, e)
	if len(request.Messages) != 1 {
		t.Fatal("current input appeared twice", request.Messages)
	}
	history, err := store.History(context.Background(), e.Scope, 20)
	if err != nil || len(history) != 1 || history[0].Event.ObserveOnly {
		t.Fatal("observation did not become one complete turn", history, err)
	}
}
