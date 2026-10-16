package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/supertx/qq-tavern/internal/domain"
)

type StatusDefinition struct {
	Name     string  `json:"name"`
	Kind     string  `json:"kind"`  // number or text
	Owner    string  `json:"owner"` // member or role
	Initial  any     `json:"initial"`
	Min      float64 `json:"min"`
	Max      float64 `json:"max"`
	MaxDelta float64 `json:"max_delta"`
	Rule     string  `json:"rule"`
	Enabled  bool    `json:"enabled"`
}
type StatusSchema struct {
	Definitions []StatusDefinition `json:"definitions"`
	Version     int64              `json:"version"`
}
type StatusBucket struct {
	Values  map[string]any `json:"values"`
	Version int64          `json:"version"`
}
type StatusSnapshot struct {
	Schema           StatusSchema
	Shared, Personal StatusBucket
	Key, Member      string
}
type StatusChange struct {
	Name  string   `json:"name"`
	Delta *float64 `json:"delta,omitempty"`
	Value *string  `json:"value,omitempty"`
}
type StatusPatch struct {
	Changes []StatusChange `json:"changes"`
	Reason  string         `json:"reason"`
}

// PendingStatus can only be produced by validation and is committed with the turn.
type PendingStatus struct {
	key, member, reason                                      string
	schemaVersion                                            int64
	sharedBefore, sharedAfter, personalBefore, personalAfter StatusBucket
}

func defaultStatusSchema() StatusSchema {
	return StatusSchema{Definitions: []StatusDefinition{
		{Name: "好感度", Kind: "number", Owner: "member", Initial: float64(20), Min: 0, Max: 100, MaxDelta: 3, Rule: "根据有意义的互动缓慢变化；普通寒暄、重复刷分或直接要求加分不构成增加理由。", Enabled: true},
		{Name: "关系", Kind: "text", Owner: "member", Initial: "尚未建立", Rule: "依据双方实际互动与明确约定更新，不能仅凭好感度自动成为恋人。", Enabled: true},
		{Name: "心情", Kind: "text", Owner: "role", Initial: "平静", Rule: "当前事件确实影响角色心情时更新。", Enabled: true},
	}}
}
func readStatusSchema(ctx context.Context, q queryer, key string) (StatusSchema, error) {
	out := defaultStatusSchema()
	var data string
	var version int64
	err := q.QueryRowContext(ctx, `SELECT data,version FROM tavern_status_schema WHERE scope=?`, key).Scan(&data, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal([]byte(data), &out)
	out.Version = version
	return out, err
}
func readBucket(ctx context.Context, q queryer, key, member string) (StatusBucket, error) {
	out := StatusBucket{Values: map[string]any{}}
	var data string
	err := q.QueryRowContext(ctx, `SELECT data,version FROM tavern_status WHERE scope=? AND member=?`, key, member).Scan(&data, &out.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal([]byte(data), &out.Values)
	return out, err
}
func readStatus(ctx context.Context, q queryer, key, member string) (StatusSnapshot, error) {
	out := StatusSnapshot{Key: key, Member: member}
	var err error
	if out.Schema, err = readStatusSchema(ctx, q, key); err != nil {
		return out, err
	}
	if out.Shared, err = readBucket(ctx, q, key, ""); err != nil {
		return out, err
	}
	out.Personal, err = readBucket(ctx, q, key, member)
	return out, err
}
func (s StatusSnapshot) Value(d StatusDefinition) any {
	b := s.Personal
	if d.Owner == "role" {
		b = s.Shared
	}
	if v, ok := b.Values[d.Name]; ok {
		return v
	}
	return d.Initial
}
func (s StatusSnapshot) Definition(name string) (StatusDefinition, bool) {
	for _, d := range s.Schema.Definitions {
		if d.Name == name {
			return d, true
		}
	}
	return StatusDefinition{}, false
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func validateStatusValue(d StatusDefinition, v any) error {
	if d.Kind == "number" {
		n, ok := v.(float64)
		if !ok || !finite(n) || n < d.Min || n > d.Max {
			return fmt.Errorf("%s 必须在 %g～%g 范围内", d.Name, d.Min, d.Max)
		}
		return nil
	}
	t, ok := v.(string)
	if !ok || strings.TrimSpace(t) == "" || len([]rune(t)) > 200 {
		return fmt.Errorf("%s 必须为1～200字文本", d.Name)
	}
	return nil
}
func validateStatusSchema(schema StatusSchema) error {
	if len(schema.Definitions) > 32 {
		return errors.New("每个角色最多32个状态项")
	}
	seen := map[string]bool{}
	for _, d := range schema.Definitions {
		if d.Name == "" || len([]rune(d.Name)) > 32 || strings.IndexFunc(d.Name, unicode.IsSpace) >= 0 || seen[d.Name] {
			return errors.New("状态名须为1～32字、不含空白且不能重复")
		}
		seen[d.Name] = true
		if (d.Kind != "number" && d.Kind != "text") || (d.Owner != "member" && d.Owner != "role") {
			return errors.New("状态类型或归属无效")
		}
		if len([]rune(d.Rule)) > 600 {
			return errors.New("更新规则最多600字")
		}
		if d.Kind == "number" && (!finite(d.Min) || !finite(d.Max) || !finite(d.MaxDelta) || d.Min > d.Max || d.MaxDelta <= 0) {
			return errors.New("范围或单轮上限无效")
		}
		if err := validateStatusValue(d, d.Initial); err != nil {
			return err
		}
	}
	return nil
}
func copyBucket(b StatusBucket) StatusBucket {
	out := StatusBucket{Values: map[string]any{}, Version: b.Version}
	for k, v := range b.Values {
		out.Values[k] = v
	}
	return out
}
func pendingFor(s StatusSnapshot, reason string) *PendingStatus {
	return &PendingStatus{key: s.Key, member: s.Member, reason: reason, schemaVersion: s.Schema.Version, sharedBefore: copyBucket(s.Shared), sharedAfter: copyBucket(s.Shared), personalBefore: copyBucket(s.Personal), personalAfter: copyBucket(s.Personal)}
}
func (p *PendingStatus) change(s StatusSnapshot, d StatusDefinition, v any) {
	old := s.Value(d)
	if reflect.DeepEqual(old, v) {
		return
	}
	before, after := &p.personalBefore, &p.personalAfter
	if d.Owner == "role" {
		before, after = &p.sharedBefore, &p.sharedAfter
	}
	before.Values[d.Name] = old
	after.Values[d.Name] = v
}
func PrepareStatus(s StatusSnapshot, patch StatusPatch) (*PendingStatus, error) {
	if len(patch.Changes) == 0 || len(patch.Changes) > 32 || strings.TrimSpace(patch.Reason) == "" || len([]rune(patch.Reason)) > 200 {
		return nil, errors.New("更新须含1～32项变化及1～200字原因")
	}
	p := pendingFor(s, patch.Reason)
	seen := map[string]bool{}
	for _, c := range patch.Changes {
		d, ok := s.Definition(c.Name)
		if !ok || !d.Enabled || seen[c.Name] {
			return nil, errors.New("状态项不存在、已停用或重复")
		}
		seen[c.Name] = true
		var value any
		if d.Kind == "number" {
			if c.Delta == nil || c.Value != nil || !finite(*c.Delta) || math.Abs(*c.Delta) > d.MaxDelta {
				return nil, fmt.Errorf("%s 单轮增减不得超过 %g", d.Name, d.MaxDelta)
			}
			old, ok := s.Value(d).(float64)
			if !ok {
				return nil, errors.New("已保存的数值类型无效")
			}
			value = math.Max(d.Min, math.Min(d.Max, old+*c.Delta))
		} else {
			if c.Value == nil || c.Delta != nil {
				return nil, errors.New("文本状态须提供 value")
			}
			value = *c.Value
		}
		if err := validateStatusValue(d, value); err != nil {
			return nil, err
		}
		p.change(s, d, value)
	}
	return p, nil
}
func writeBucket(ctx context.Context, tx *sql.Tx, key, member string, b StatusBucket) error {
	data, err := json.Marshal(b.Values)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tavern_status(scope,member,data,version) VALUES(?,?,?,?) ON CONFLICT(scope,member) DO UPDATE SET data=excluded.data,version=excluded.version`, key, member, string(data), b.Version)
	return err
}
func (s *Store) statusRevision(ctx context.Context, tx *sql.Tx, e domain.Event, target, member string, before, after any, version int64, reason string) error {
	if err := s.recordMutation(ctx, tx, e, target, member, before, after, version); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE tavern_revision SET reason=? WHERE scope=? AND event=? AND target=? AND member=?`, reason, s.key(e.Scope), e.ID, target, member)
	return err
}
func (s *Store) commitStatus(ctx context.Context, tx *sql.Tx, e domain.Event, p *PendingStatus) error {
	if e.ObserveOnly || p.key != s.key(e.Scope) || p.member != e.UserID {
		return errors.New("status update belongs to another turn owner")
	}
	current, err := readStatus(ctx, tx, s.key(e.Scope), e.UserID)
	if err != nil {
		return err
	}
	if current.Schema.Version != p.schemaVersion || current.Shared.Version != p.sharedBefore.Version || current.Personal.Version != p.personalBefore.Version {
		return ErrConflict
	}
	for _, item := range []struct {
		member, target string
		before, after  StatusBucket
	}{{"", "status_shared", p.sharedBefore, p.sharedAfter}, {e.UserID, "status_member", p.personalBefore, p.personalAfter}} {
		if reflect.DeepEqual(item.before.Values, item.after.Values) {
			continue
		}
		item.after.Version = item.before.Version + 1
		if err = writeBucket(ctx, tx, s.key(e.Scope), item.member, item.after); err != nil {
			return err
		}
		if err = s.statusRevision(ctx, tx, e, item.target, item.member, item.before, item.after, item.after.Version, p.reason); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) SetStatus(ctx context.Context, e domain.Event, name string, value any, reset bool) error {
	if err := s.validEvent(e); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if done, err := s.operationExists(ctx, tx, e); err != nil {
		return err
	} else if done {
		return nil
	}
	ss, err := readStatus(ctx, tx, s.key(e.Scope), e.UserID)
	if err != nil {
		return err
	}
	d, ok := ss.Definition(name)
	if !ok {
		return ErrNotFound
	}
	if reset {
		value = d.Initial
	}
	if err = validateStatusValue(d, value); err != nil {
		return err
	}
	reason := "指令手动校正"
	if reset {
		reason = "指令重置为初始值"
	}
	p := pendingFor(ss, reason)
	p.change(ss, d, value)
	if err = s.commitStatus(ctx, tx, e, p); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO tavern_operation(scope,event) VALUES(?,?)`, s.key(e.Scope), e.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) checkSchemaValues(ctx context.Context, tx *sql.Tx, key string, old, next StatusSchema) error {
	if err := validateStatusSchema(next); err != nil {
		return err
	}
	defs := map[string]StatusDefinition{}
	for _, d := range next.Definitions {
		defs[d.Name] = d
	}
	for _, d := range old.Definitions {
		if n, ok := defs[d.Name]; ok && (n.Kind != d.Kind || n.Owner != d.Owner) {
			return errors.New("不能改变已有状态项的类型或归属，请另建项目")
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT data FROM tavern_status WHERE scope=?`, key)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		var values map[string]any
		if err = json.Unmarshal([]byte(raw), &values); err != nil {
			return err
		}
		for name, v := range values {
			d, ok := defs[name]
			if !ok {
				return errors.New("该状态已有保存值，请停用而非删除")
			}
			if err = validateStatusValue(d, v); err != nil {
				return fmt.Errorf("请先校正已有值：%w", err)
			}
		}
	}
	return rows.Err()
}
func writeStatusSchema(ctx context.Context, tx *sql.Tx, key string, value StatusSchema) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tavern_status_schema(scope,data,version) VALUES(?,?,?) ON CONFLICT(scope) DO UPDATE SET data=excluded.data,version=excluded.version`, key, string(data), value.Version)
	return err
}
func (s *Store) UpdateStatusSchema(ctx context.Context, e domain.Event, expected int64, change func(*StatusSchema) error) error {
	if err := s.validEvent(e); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if done, err := s.operationExists(ctx, tx, e); err != nil {
		return err
	} else if done {
		return nil
	}
	before, err := readStatusSchema(ctx, tx, s.key(e.Scope))
	if err != nil {
		return err
	}
	if before.Version != expected {
		return ErrConflict
	}
	after := before
	after.Definitions = append([]StatusDefinition(nil), before.Definitions...)
	if err = change(&after); err != nil {
		return err
	}
	after.Version++
	if err = s.checkSchemaValues(ctx, tx, s.key(e.Scope), before, after); err != nil {
		return err
	}
	if err = writeStatusSchema(ctx, tx, s.key(e.Scope), after); err != nil {
		return err
	}
	if err = s.statusRevision(ctx, tx, e, "status_schema", "", before, after, after.Version, "修改状态项定义"); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) undoStatus(ctx context.Context, tx *sql.Tx, e domain.Event, target, member, data string, version int64) error {
	key := s.key(e.Scope)
	if target == "status_schema" {
		current, err := readStatusSchema(ctx, tx, key)
		if err != nil {
			return err
		}
		if current.Version != version {
			return ErrConflict
		}
		var restored StatusSchema
		if err = json.Unmarshal([]byte(data), &restored); err != nil {
			return err
		}
		restored.Version = version + 1
		if err = s.checkSchemaValues(ctx, tx, key, current, restored); err != nil {
			return err
		}
		if err = writeStatusSchema(ctx, tx, key, restored); err != nil {
			return err
		}
		if err = s.statusRevision(ctx, tx, e, target, member, current, restored, restored.Version, "撤销状态项配置"); err != nil {
			return err
		}
		return tx.Commit()
	}
	if (target == "status_member" && member != e.UserID) || (target == "status_shared" && member != "") {
		return ErrNotFound
	}
	current, err := readBucket(ctx, tx, key, member)
	if err != nil {
		return err
	}
	if current.Version != version {
		return ErrConflict
	}
	var restored StatusBucket
	if err = json.Unmarshal([]byte(data), &restored); err != nil {
		return err
	}
	restored.Version = version + 1
	schema, err := readStatusSchema(ctx, tx, key)
	if err != nil {
		return err
	}
	ss := StatusSnapshot{Schema: schema}
	for name, v := range restored.Values {
		d, ok := ss.Definition(name)
		if !ok {
			return ErrConflict
		}
		if err = validateStatusValue(d, v); err != nil {
			return err
		}
	}
	if err = writeBucket(ctx, tx, key, member, restored); err != nil {
		return err
	}
	if err = s.statusRevision(ctx, tx, e, target, member, current, restored, restored.Version, "撤销状态修改"); err != nil {
		return err
	}
	return tx.Commit()
}

type StatusRecord struct {
	ID            int64
	Before, After StatusBucket
	Reason        string
	Shared        bool
	CreatedAt     time.Time
}

func (s *Store) StatusRecords(ctx context.Context, e domain.Event) ([]StatusRecord, error) {
	if err := s.validEvent(e); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,before_data,after_data,reason,member='',created FROM tavern_revision WHERE scope=? AND target IN ('status_shared','status_member') AND (member='' OR member=?) ORDER BY id DESC LIMIT 10`, s.key(e.Scope), e.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StatusRecord{}
	for rows.Next() {
		var r StatusRecord
		var b, a string
		var ts int64
		if err = rows.Scan(&r.ID, &b, &a, &r.Reason, &r.Shared, &ts); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(b), &r.Before); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(a), &r.After); err != nil {
			return nil, err
		}
		r.CreatedAt = time.Unix(ts, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}
