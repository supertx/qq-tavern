package qq

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/supertx/qq-tavern/internal/domain"
)

type payload struct {
	ID       string          `json:"id"`
	Op       int             `json:"op"`
	Sequence *int64          `json:"s"`
	Type     string          `json:"t"`
	Data     json.RawMessage `json:"d"`
}
type author struct {
	ID       string `json:"id"`
	MemberID string `json:"member_openid"`
	UserID   string `json:"user_openid"`
	Name     string `json:"username"`
	Bot      bool   `json:"bot"`
}

func (a author) id(group bool) string {
	if group && a.MemberID != "" {
		return a.MemberID
	}
	if !group && a.UserID != "" {
		return a.UserID
	}
	return a.ID
}

type attachment struct {
	URL    string `json:"url"`
	Type   string `json:"content_type"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}
type element struct {
	Author      author       `json:"author"`
	Type        int          `json:"message_type"`
	Content     string       `json:"content"`
	Attachments []attachment `json:"attachments"`
	Elements    []element    `json:"msg_elements"`
}
type message struct {
	ID          string       `json:"id"`
	GroupID     string       `json:"group_openid"`
	Author      author       `json:"author"`
	Content     string       `json:"content"`
	Timestamp   string       `json:"timestamp"`
	Attachments []attachment `json:"attachments"`
	Elements    []element    `json:"msg_elements"`
	Reference   *struct {
		MessageID string `json:"message_id"`
	} `json:"message_reference"`
}

// ParseEvent consumes only QQ-provided identity fields; a nickname never establishes identity.
func ParseEvent(raw []byte, botID string, received time.Time, observe bool) (*domain.Event, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Op != 0 {
		return nil, nil
	}
	isGroup := p.Type == "GROUP_AT_MESSAGE_CREATE" || p.Type == "GROUP_MESSAGE_CREATE"
	if !isGroup && p.Type != "C2C_MESSAGE_CREATE" {
		return nil, nil
	}
	if p.Type == "GROUP_MESSAGE_CREATE" && !observe {
		return nil, nil
	}
	var m message
	if err := json.Unmarshal(p.Data, &m); err != nil {
		return nil, err
	}
	if m.Author.Bot {
		return nil, nil
	}
	created := received
	if m.Timestamp != "" {
		parsed, err := time.Parse(time.RFC3339Nano, m.Timestamp)
		if err != nil {
			return nil, errors.New("invalid QQ event timestamp")
		}
		if parsed.Before(created) {
			created = parsed
		}
	}
	e := &domain.Event{MessageID: m.ID, UserID: m.Author.id(isGroup), UserName: m.Author.Name, Text: strings.TrimSpace(m.Content), Images: images(m.Attachments), ObserveOnly: p.Type == "GROUP_MESSAGE_CREATE", CreatedAt: created, Deadline: created.Add(285 * time.Second)}
	if isGroup {
		e.Scope = domain.Scope{BotID: botID, Kind: "group", ID: m.GroupID}
	} else {
		e.Scope = domain.Scope{BotID: botID, Kind: "private", ID: e.UserID}
	}
	// QQ can redeliver one message with a different dispatch ID. Observation must
	// not consume the reply event when both notification forms are enabled.
	mode := "reply"
	if e.ObserveOnly {
		mode = "observe"
	}
	e.ID = domain.Key(e.Scope.Key(), m.ID, mode)
	var visit func([]element, int, bool)
	visit = func(elements []element, depth int, quoted bool) {
		if depth > 5 {
			return
		}
		for _, item := range elements {
			if len(e.Quotes) >= 20 {
				return
			}
			q := quoted || item.Type == 103 || item.Type == 102
			if q && (item.Content != "" || len(item.Attachments) > 0) {
				e.Quotes = append(e.Quotes, domain.Quote{AuthorID: item.Author.id(isGroup), AuthorName: item.Author.Name, Text: item.Content, Images: images(item.Attachments)})
			}
			visit(item.Elements, depth+1, q)
		}
	}
	visit(m.Elements, 0, false)
	if len(e.Quotes) == 0 && m.Reference != nil && m.Reference.MessageID != "" {
		e.Quotes = append(e.Quotes, domain.Quote{Text: "[引用消息正文未由 QQ 提供]"})
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if !received.Before(e.Deadline) {
		return nil, nil
	}
	return e, nil
}

func images(items []attachment) []string {
	var result []string
	seen := map[string]bool{}
	for _, item := range items {
		if len(result) == 10 {
			break
		}
		if !strings.HasPrefix(strings.ToLower(item.Type), "image/") && !(item.Width > 0 && item.Height > 0) {
			continue
		}
		raw := item.URL
		if strings.HasPrefix(raw, "//") {
			raw = "https:" + raw
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || seen[raw] {
			continue
		}
		seen[raw] = true
		result = append(result, raw)
	}
	return result
}
