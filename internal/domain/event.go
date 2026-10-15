package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

type Scope struct {
	BotID string `json:"bot_id"`
	Kind  string `json:"kind"`
	ID    string `json:"id"`
}

func Key(parts ...string) string {
	v := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(v[:])
}
func (s Scope) Key() string { return Key(s.BotID, s.Kind, s.ID) }
func (s Scope) Validate() error {
	if s.BotID == "" || s.ID == "" || (s.Kind != "group" && s.Kind != "private") {
		return errors.New("invalid conversation scope")
	}
	return nil
}

type Quote struct {
	AuthorID   string   `json:"author_id,omitempty"`
	AuthorName string   `json:"author_name,omitempty"`
	Text       string   `json:"text"`
	Images     []string `json:"images,omitempty"`
}
type Event struct {
	ID          string    `json:"id"`
	MessageID   string    `json:"message_id"`
	Scope       Scope     `json:"scope"`
	UserID      string    `json:"user_id"`
	UserName    string    `json:"user_name"`
	Text        string    `json:"text"`
	Images      []string  `json:"images,omitempty"`
	Quotes      []Quote   `json:"quotes,omitempty"`
	ObserveOnly bool      `json:"observe_only,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	Deadline    time.Time `json:"deadline"`
}

func (e Event) Validate() error {
	if err := e.Scope.Validate(); err != nil {
		return err
	}
	if e.ID == "" || e.MessageID == "" || e.UserID == "" || e.Deadline.IsZero() {
		return errors.New("invalid event identity or deadline")
	}
	if e.Scope.Kind == "private" && e.Scope.ID != e.UserID {
		return errors.New("private scope must match sender")
	}
	return nil
}
func (e Event) MemberKey() string { return Key(e.Scope.Key(), e.UserID)[:16] }

type Media struct {
	URL  string `json:"url,omitempty"`
	Data []byte `json:"data,omitempty"`
	MIME string `json:"mime"`
	Kind string `json:"kind"`
}
type SendRequest struct {
	Event Event  `json:"event"`
	Key   string `json:"key"`
	Text  string `json:"text,omitempty"`
	Media *Media `json:"media,omitempty"`
}
