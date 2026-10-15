package state

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/supertx/qq-tavern/internal/domain"
)

var ErrConflict = errors.New("settings changed; read the current version and retry")
var ErrNotFound = errors.New("record not found in this conversation")

type Character struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Personality string `json:"personality"`
	Scenario    string `json:"scenario"`
}
type Entry struct {
	ID       int      `json:"id"`
	Title    string   `json:"title"`
	Content  string   `json:"content"`
	Keywords []string `json:"keywords,omitempty"`
	Constant bool     `json:"constant"`
	Enabled  bool     `json:"enabled"`
	Priority int      `json:"priority"`
}
type Config struct {
	Character Character `json:"character"`
	Note      string    `json:"note"`
	Entries   []Entry   `json:"entries"`
	Budget    int       `json:"budget_chars"`
	Version   int64     `json:"version"`
}
type Summary struct {
	Text       string `json:"text"`
	Checkpoint int64  `json:"checkpoint"`
	Version    int64  `json:"version"`
}
type Activation struct {
	ID       int    `json:"id"`
	Title    string `json:"title"`
	Reason   string `json:"reason"`
	Injected bool   `json:"injected"`
}
type Snapshot struct {
	Config          Config
	Personal        string
	PersonalVersion int64
	Summary         Summary
	LastActivation  []Activation
}
type Revision struct {
	ID        int64
	Target    string
	ActorID   string
	ActorName string
	EventID   string
	Before    json.RawMessage
	After     json.RawMessage
	Version   int64
	CreatedAt time.Time
}
type Turn struct {
	ID        int64
	Event     domain.Event
	Model     string
	Payload   json.RawMessage
	CreatedAt time.Time
}
