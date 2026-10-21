package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/supertx/qq-tavern/internal/domain"
)

const MaxImportBytes = 8 << 20

type CharacterPatch struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Personality *string `json:"personality,omitempty"`
	Scenario    *string `json:"scenario,omitempty"`
}
type ImportEntry struct {
	Title    string   `json:"title"`
	Content  string   `json:"content"`
	Keywords []string `json:"keywords,omitempty"`
	Constant bool     `json:"constant"`
	Enabled  *bool    `json:"enabled,omitempty"`
	Priority *int     `json:"priority,omitempty"`
}
type WorldbookPatch struct {
	Entries *[]ImportEntry `json:"entries,omitempty"`
	Budget  *int           `json:"budget_chars,omitempty"`
}
type ConfigPatch struct {
	Character *CharacterPatch `json:"character,omitempty"`
	Note      *string         `json:"note,omitempty"`
	Worldbook *WorldbookPatch `json:"worldbook,omitempty"`
}
type BundleRole struct {
	Name string `json:"name"`
	ConfigPatch
}
type Bundle struct {
	Format  string       `json:"format"`
	Version int          `json:"version"`
	Roles   []BundleRole `json:"roles"`
}

// DecodeJSON rejects ambiguous duplicate keys, nulls, unknown fields, invalid
// UTF-8 and trailing documents. Paths in validation errors never echo content.
func DecodeJSON(data []byte, target any) error {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(data) {
		return errors.New("$: document must be UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var walk func(string, int, reflect.Type) error
	walk = func(path string, depth int, typ reflect.Type) error {
		for typ != nil && typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if depth > 32 {
			return fmt.Errorf("%s: JSON nesting is too deep", path)
		}
		token, err := d.Token()
		if err != nil {
			return fmt.Errorf("%s: invalid JSON: %w", path, err)
		}
		if token == nil {
			return fmt.Errorf("%s: null is not supported; omit unchanged fields", path)
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				t, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := t.(string)
				if !ok {
					return fmt.Errorf("%s: invalid object key", path)
				}
				if seen[key] {
					return fmt.Errorf("%s.%s: duplicate field", path, key)
				}
				seen[key] = true
				var fieldType reflect.Type
				if typ != nil && typ.Kind() == reflect.Struct {
					var found bool
					fieldType, found = jsonFieldType(typ, key)
					if !found {
						return fmt.Errorf("%s.%s: unknown field", path, key)
					}
				} else if typ != nil && typ.Kind() == reflect.Map {
					fieldType = typ.Elem()
				}
				if err = walk(path+"."+key, depth+1, fieldType); err != nil {
					return err
				}
			}
			_, err = d.Token()
		case json.Delim('['):
			var itemType reflect.Type
			if typ != nil && (typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array) {
				itemType = typ.Elem()
			}
			for i := 0; d.More(); i++ {
				if err = walk(fmt.Sprintf("%s[%d]", path, i), depth+1, itemType); err != nil {
					return err
				}
			}
			_, err = d.Token()
		}
		return err
	}
	if err := walk("$", 0, reflect.TypeOf(target)); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("$: expected exactly one JSON document")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(target)
}

// Match exact JSON field names, including the embedded patch structs used by
// bundle roles and admin requests, so errors identify the offending array item.
func jsonFieldType(typ reflect.Type, key string) (reflect.Type, bool) {
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == key {
			return field.Type, true
		}
		if field.Anonymous && name == "" && field.Type.Kind() == reflect.Struct {
			if nested, ok := jsonFieldType(field.Type, key); ok {
				return nested, true
			}
		}
	}
	return nil, false
}

func ParseBundle(data []byte) (Bundle, error) {
	var b Bundle
	if len(data) > MaxImportBytes {
		return b, errors.New("document exceeds 8 MiB")
	}
	if err := DecodeJSON(data, &b); err != nil {
		return b, err
	}
	if b.Format != "qq-tavern.bundle" || b.Version != 1 {
		return b, errors.New("$.format/version: expected qq-tavern.bundle version 1")
	}
	if len(b.Roles) < 1 || len(b.Roles) > 100 {
		return b, errors.New("$.roles: expected 1–100 roles")
	}
	seen := map[string]bool{}
	for i, r := range b.Roles {
		if err := validateRoleName(r.Name); err != nil {
			return b, fmt.Errorf("$.roles[%d].name: %w", i, err)
		}
		if seen[r.Name] {
			return b, fmt.Errorf("$.roles[%d].name: duplicate role", i)
		}
		seen[r.Name] = true
		if r.Character != nil && r.Character.Name != nil && *r.Character.Name != r.Name {
			return b, fmt.Errorf("$.roles[%d].character.name: must equal name; use the config API to rename", i)
		}
	}
	return b, nil
}

func (p ConfigPatch) Groups() []CommandGroup {
	var groups []CommandGroup
	if p.Character != nil && (p.Character.Name != nil || p.Character.Description != nil || p.Character.Personality != nil || p.Character.Scenario != nil) {
		groups = append(groups, RoleEdit)
	}
	if p.Note != nil || (p.Worldbook != nil && (p.Worldbook.Entries != nil || p.Worldbook.Budget != nil)) {
		groups = append(groups, WorldEdit)
	}
	return groups
}

func (p ConfigPatch) Fields() []string {
	fields := []string{}
	if p.Character != nil {
		for _, v := range []struct {
			name string
			p    *string
		}{{"name", p.Character.Name}, {"description", p.Character.Description}, {"personality", p.Character.Personality}, {"scenario", p.Character.Scenario}} {
			if v.p != nil {
				fields = append(fields, "character."+v.name)
			}
		}
	}
	if p.Note != nil {
		fields = append(fields, "note")
	}
	if p.Worldbook != nil {
		if p.Worldbook.Entries != nil {
			fields = append(fields, "worldbook.entries")
		}
		if p.Worldbook.Budget != nil {
			fields = append(fields, "worldbook.budget_chars")
		}
	}
	return fields
}

func (p ConfigPatch) Apply(before Config) (Config, error) {
	after := before
	if p.Character != nil {
		for _, pair := range []struct{ from, to *string }{
			{p.Character.Name, &after.Character.Name},
			{p.Character.Description, &after.Character.Description},
			{p.Character.Personality, &after.Character.Personality},
			{p.Character.Scenario, &after.Character.Scenario},
		} {
			if pair.from != nil {
				*pair.to = *pair.from
			}
		}
		if p.Character.Name != nil && strings.TrimSpace(*p.Character.Name) != "" {
			if err := validateRoleName(*p.Character.Name); err != nil {
				return before, fmt.Errorf("character.name: %w", err)
			}
			after.ProfileName = *p.Character.Name
		}
	}
	if p.Note != nil {
		after.Note = *p.Note
	}
	if p.Worldbook != nil {
		if p.Worldbook.Budget != nil {
			after.Budget = *p.Worldbook.Budget
		}
		if p.Worldbook.Entries != nil {
			after.Entries = []Entry{}
			for i, input := range *p.Worldbook.Entries {
				if strings.TrimSpace(input.Title) == "" {
					return before, fmt.Errorf("worldbook.entries[%d].title: required", i)
				}
				e := Entry{ID: i + 1, Title: input.Title, Content: input.Content, Keywords: input.Keywords, Constant: input.Constant, Enabled: true, Priority: 50}
				if input.Enabled != nil {
					e.Enabled = *input.Enabled
				}
				if input.Priority != nil {
					e.Priority = *input.Priority
				}
				if err := validateConfig(Config{Entries: []Entry{e}}); err != nil {
					return before, fmt.Errorf("worldbook.entries[%d]: %w", i, err)
				}
				after.Entries = append(after.Entries, e)
			}
		}
	}
	return after, validateConfig(after)
}

type ImportChange struct {
	Name             string   `json:"name"`
	RoleID           int64    `json:"role_id"`
	Action           string   `json:"action"`
	Fields           []string `json:"fields"`
	WorldbookEntries int      `json:"worldbook_entries"`
	Version          int64    `json:"version"`
}
type ImportPreview struct {
	ID        string         `json:"id"`
	ExpiresAt time.Time      `json:"expires_at"`
	Changes   []ImportChange `json:"changes"`
}
type plannedRole struct {
	RoleID   int64          `json:"role_id"`
	Expected int64          `json:"expected"`
	Config   Config         `json:"config"`
	Groups   []CommandGroup `json:"groups"`
}
type importPlan struct {
	Preview ImportPreview `json:"preview"`
	Roles   []plannedRole `json:"roles"`
}

// source is supplied by the trusted entrypoint ("qq" or "admin"), not the bundle.
func (s *Store) PreviewImport(ctx context.Context, scope domain.Scope, actor, source string, data []byte) (ImportPreview, error) {
	if err := s.checkScope(scope); err != nil {
		return ImportPreview{}, err
	}
	if (source != "qq" && source != "admin") || actor == "" || (source == "qq" && scope.Kind == "private" && actor != scope.ID) {
		return ImportPreview{}, errors.New("invalid import actor")
	}
	b, err := ParseBundle(data)
	if err != nil {
		return ImportPreview{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportPreview{}, err
	}
	defer tx.Rollback()
	plan := importPlan{Preview: ImportPreview{ID: uuid.NewString(), ExpiresAt: time.Now().UTC().Add(10 * time.Minute), Changes: []ImportChange{}}}
	for i, item := range b.Roles {
		r, err := scanRole(tx.QueryRowContext(ctx, `SELECT id,storage_key,name FROM tavern_role WHERE scope=? AND name=?`, scope.Key(), item.Name), scope)
		c := Config{ProfileName: item.Name, Character: Character{Name: item.Name}, Budget: 6000, Entries: []Entry{}}
		groups := item.Groups()
		action := "create"
		if err == nil {
			c, err = readConfig(ctx, tx, r.Key)
			if err != nil {
				return ImportPreview{}, err
			}
			action = "update"
		} else if errors.Is(err, ErrNotFound) {
			groups = append(groups, RoleEdit)
		} else {
			return ImportPreview{}, err
		}
		if source == "qq" {
			if err = checkGroups(ctx, tx, groups...); err != nil {
				return ImportPreview{}, err
			}
		}
		after, err := item.ConfigPatch.Apply(c)
		if err != nil {
			return ImportPreview{}, fmt.Errorf("$.roles[%d]: %w", i, err)
		}
		if action == "update" && len(item.Fields()) == 0 {
			return ImportPreview{}, fmt.Errorf("$.roles[%d]: no settings provided", i)
		}
		plan.Roles = append(plan.Roles, plannedRole{RoleID: r.ID, Expected: c.Version, Config: after, Groups: groups})
		plan.Preview.Changes = append(plan.Preview.Changes, ImportChange{Name: item.Name, RoleID: r.ID, Action: action, Fields: item.Fields(), WorldbookEntries: len(after.Entries), Version: c.Version})
	}
	// Expired previews retain only their receipt, not their potentially large data.
	if _, err = tx.ExecContext(ctx, `UPDATE tavern_import SET payload='',status='expired' WHERE status='pending' AND expires<=?`, time.Now().Unix()); err != nil {
		return ImportPreview{}, err
	}
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM tavern_import WHERE scope=? AND actor=? AND source=? AND status='pending'`, scope.Key(), actor, source).Scan(&pending); err != nil {
		return ImportPreview{}, err
	}
	if pending >= 8 {
		return ImportPreview{}, errors.New("最多保留 8 个待确认导入，请先确认或取消")
	}
	raw, _ := json.Marshal(plan)
	_, err = tx.ExecContext(ctx, `INSERT INTO tavern_import(id,scope,actor,source,payload,status,created,expires) VALUES(?,?,?,?,?,'pending',?,?)`, plan.Preview.ID, scope.Key(), actor, source, string(raw), time.Now().Unix(), plan.Preview.ExpiresAt.Unix())
	if err != nil {
		return ImportPreview{}, err
	}
	return plan.Preview, tx.Commit()
}

func (s *Store) CommitImport(ctx context.Context, scope domain.Scope, actor, source, id string) (ImportPreview, error) {
	if err := s.checkScope(scope); err != nil {
		return ImportPreview{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportPreview{}, err
	}
	defer tx.Rollback()
	var raw, status, result string
	var expires int64
	err = tx.QueryRowContext(ctx, `SELECT payload,status,result,expires FROM tavern_import WHERE id=? AND scope=? AND actor=? AND source=?`, id, scope.Key(), actor, source).Scan(&raw, &status, &result, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return ImportPreview{}, ErrNotFound
	}
	if err != nil {
		return ImportPreview{}, err
	}
	var plan importPlan
	if status == "committed" {
		var receipt ImportPreview
		err = json.Unmarshal([]byte(result), &receipt)
		return receipt, err
	}
	if status != "pending" || time.Now().Unix() >= expires {
		return ImportPreview{}, ErrExpiredImport
	}
	if err = json.Unmarshal([]byte(raw), &plan); err != nil {
		return ImportPreview{}, err
	}
	for i, item := range plan.Roles {
		if source == "qq" {
			if err = checkGroups(ctx, tx, item.Groups...); err != nil {
				return ImportPreview{}, err
			}
		}
		var r Role
		var before Config
		after := item.Config
		after.Version = item.Expected + 1
		if item.RoleID == 0 {
			before = Config{ProfileName: after.ProfileName, Character: Character{Name: after.ProfileName}, Budget: 6000, Entries: []Entry{}}
			r, err = createConfiguredRole(ctx, tx, scope, after)
		} else {
			r, err = scanRole(tx.QueryRowContext(ctx, `SELECT id,storage_key,name FROM tavern_role WHERE scope=? AND id=?`, scope.Key(), item.RoleID), scope)
			if err == nil {
				before, err = readConfig(ctx, tx, r.Key)
			}
			if err == nil && (before.Version != item.Expected || r.Name != after.ProfileName) {
				err = ErrConflict
			}
			if err == nil {
				err = writeConfig(ctx, tx, scope, r.Key, after)
			}
		}
		if err != nil {
			return ImportPreview{}, err
		}
		if source == "admin" {
			err = adminRevision(ctx, tx, r, "config", before, after, after.Version)
		} else {
			bound := &Store{db: s.db, role: &r}
			e := domain.Event{ID: "import:" + id, Scope: scope, UserID: actor, UserName: "QQ 文件导入"}
			if item.RoleID == 0 {
				before = Config{ProfileName: after.ProfileName, Character: Character{Name: after.ProfileName}, Budget: 6000, Entries: []Entry{}}
			}
			err = bound.recordMutation(ctx, tx, e, "config", "", before, after, after.Version)
		}
		if err != nil {
			return ImportPreview{}, err
		}
		plan.Preview.Changes[i].RoleID = r.ID
		plan.Preview.Changes[i].Version = after.Version
	}
	resultData, _ := json.Marshal(plan.Preview)
	if _, err = tx.ExecContext(ctx, `UPDATE tavern_import SET status='committed',payload='',result=? WHERE id=?`, string(resultData), id); err != nil {
		return ImportPreview{}, err
	}
	return plan.Preview, tx.Commit()
}

func (s *Store) CancelImport(ctx context.Context, scope domain.Scope, actor, source, id string) error {
	if err := s.checkScope(scope); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE tavern_import SET status='cancelled',payload='' WHERE id=? AND scope=? AND actor=? AND source=? AND status IN ('pending','cancelled')`, id, scope.Key(), actor, source)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}
