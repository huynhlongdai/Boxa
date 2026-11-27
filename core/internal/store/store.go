// Package store persists Boxmes state. One portable SQL implementation runs on
// SQLite (modernc.org/sqlite, pure Go, default) and Postgres (pgx stdlib,
// selected when DATABASE_URL starts with postgres:// or postgresql://).
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"boxmes/core/internal/model"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// TimeFormat is fixed-width so string comparison == time comparison.
const TimeFormat = "2006-01-02T15:04:05.000000Z"

func FmtTime(t time.Time) string { return t.UTC().Format(TimeFormat) }

// Store is the persistence interface used by the rest of core.
type Store interface {
	Tenancy

	Close() error
	Dialect() string
	Ping(ctx context.Context) error

	PutAgent(ctx context.Context, a *model.Agent) error
	GetAgent(ctx context.Context, id string) (*model.Agent, error)
	GetAgentByName(ctx context.Context, name string) (*model.Agent, error)
	ListAgents(ctx context.Context) ([]*model.Agent, error)
	DeleteAgent(ctx context.Context, id string) error

	PutKey(ctx context.Context, k *model.APIKey) error
	GetKeyByHash(ctx context.Context, hash string) (*model.APIKey, error)
	GetKey(ctx context.Context, id string) (*model.APIKey, error)
	ListKeys(ctx context.Context, agentID string) ([]*model.APIKey, error)

	PutBox(ctx context.Context, b *model.Box) error
	GetBox(ctx context.Context, id string) (*model.Box, error)
	ListBoxes(ctx context.Context, f BoxFilter) ([]*model.Box, error)

	PutHost(ctx context.Context, h *model.Host) error
	GetHost(ctx context.Context, id string) (*model.Host, error)
	GetHostByToken(ctx context.Context, hash string) (*model.Host, error)
	GetHostByBootstrap(ctx context.Context, hash string) (*model.Host, error)
	ListHosts(ctx context.Context) ([]*model.Host, error)
	DeleteHost(ctx context.Context, id string) error

	PutProvider(ctx context.Context, p *model.Provider) error
	GetProvider(ctx context.Context, id string) (*model.Provider, error)
	ListProviders(ctx context.Context) ([]*model.Provider, error)

	ReplaceOffers(ctx context.Context, provider, source string, offers []model.Offer) error
	ListOffers(ctx context.Context, provider string) ([]model.Offer, error)
	PutCapabilities(ctx context.Context, provider, source string, caps map[string]any) error
	GetCapabilities(ctx context.Context, provider string) (map[string]any, error)

	PutJob(ctx context.Context, j *model.Job) error
	GetJob(ctx context.Context, id string) (*model.Job, error)
	ListJobs(ctx context.Context, boxID string, limit int) ([]*model.Job, error)

	AddEvent(ctx context.Context, e *model.Event) error
	ListEvents(ctx context.Context, f EventFilter) ([]*model.Event, error)

	AddLedger(ctx context.Context, l *model.LedgerEntry) error
	ListLedger(ctx context.Context, f LedgerFilter) ([]*model.LedgerEntry, error)
	SumLedger(ctx context.Context, f LedgerFilter) (float64, error)
	SumLedgerBy(ctx context.Context, f LedgerFilter, groupExpr string) (map[string]float64, error)
	SumLedgerByDateAgent(ctx context.Context, from time.Time, workspaceID string) ([]DateAgentSum, error)

	PutTemplate(ctx context.Context, t *model.Template) error
	GetTemplate(ctx context.Context, name string) (*model.Template, error)
	ListTemplates(ctx context.Context) ([]*model.Template, error)
	DeleteTemplate(ctx context.Context, name string) error
	// v2.0: workspace templates shadow the built-in ones of the same name.
	GetTemplateIn(ctx context.Context, workspaceID, name string) (*model.Template, error)
	ListTemplatesIn(ctx context.Context, workspaceID string) ([]*model.Template, error)
	DeleteTemplateIn(ctx context.Context, workspaceID, name string) error

	PutSecret(ctx context.Context, s *model.Secret) error
	GetSecret(ctx context.Context, name string) (*model.Secret, error)
	ListSecrets(ctx context.Context) ([]*model.Secret, error)
	DeleteSecret(ctx context.Context, name string) error
	// v2.0: secret names are unique per workspace.
	GetSecretIn(ctx context.Context, workspaceID, name string) (*model.Secret, error)
	DeleteSecretIn(ctx context.Context, workspaceID, name string) error

	PutSnapshot(ctx context.Context, s *model.Snapshot) error
	ListSnapshots(ctx context.Context, boxID string) ([]*model.Snapshot, error)
	GetSnapshot(ctx context.Context, id string) (*model.Snapshot, error)
	DeleteSnapshot(ctx context.Context, id string) error

	PutApproval(ctx context.Context, a *model.Approval) error
	GetApproval(ctx context.Context, id string) (*model.Approval, error)
	FindApprovalByDedupe(ctx context.Context, key string) (*model.Approval, error)
	ListApprovals(ctx context.Context, status string) ([]*model.Approval, error)
	// ListApprovalsIn lists one workspace's approvals ("" = every workspace).
	ListApprovalsIn(ctx context.Context, workspaceID, status string) ([]*model.Approval, error)

	GetIdempotent(ctx context.Context, key string) (string, error)
	PutIdempotent(ctx context.Context, key, boxID string) error

	PutLog(ctx context.Context, id, boxID, hostID string) error
	GetLogHost(ctx context.Context, id string) (boxID, hostID string, err error)

	GetKV(ctx context.Context, key string, out any) error
	PutKV(ctx context.Context, key string, v any) error
}

type BoxFilter struct {
	AgentID, Status, Provider, Lifecycle, HostID string
	WorkspaceID                                  string
	Statuses                                     []string
	IncludeTerminated                            bool
}

type EventFilter struct {
	BoxID, AgentID, Actor, Type string
	WorkspaceID                 string // v2.0: "" = every workspace
	Limit                       int
}

type LedgerFilter struct {
	AgentID, BoxID, Provider, Billing string
	WorkspaceID                       string // v2.0: "" = every workspace
	From, To                          time.Time
	Limit                             int
}

type DateAgentSum struct {
	Date    string
	AgentID string
	Amount  float64
}

// SQL is the shared implementation.
type SQL struct {
	db      *sql.DB
	dialect string // "sqlite" | "postgres"
}

// Open picks the driver from the DSN. Empty or file path → SQLite.
func Open(dsn string) (*SQL, error) {
	s := &SQL{}
	var err error
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		s.dialect = "postgres"
		s.db, err = sql.Open("pgx", dsn)
		if err != nil {
			return nil, err
		}
		s.db.SetMaxOpenConns(10)
	} else {
		s.dialect = "sqlite"
		path := strings.TrimPrefix(dsn, "sqlite://")
		if path == "" {
			path = "boxmes.db"
		}
		q := "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"
		if path == ":memory:" {
			q = "?_pragma=busy_timeout(10000)"
		}
		s.db, err = sql.Open("sqlite", "file:"+path+q)
		if err != nil {
			return nil, err
		}
		// SQLite: a single writer connection avoids SQLITE_BUSY storms.
		s.db.SetMaxOpenConns(1)
	}
	if err := s.migrate(context.Background()); err != nil {
		s.db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if _, err := s.BackfillDefaultWorkspace(context.Background()); err != nil {
		s.db.Close()
		return nil, fmt.Errorf("backfill default workspace: %w", err)
	}
	return s, nil
}

func (s *SQL) Close() error                   { return s.db.Close() }
func (s *SQL) Dialect() string                { return s.dialect }
func (s *SQL) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *SQL) migrate(ctx context.Context) error { return s.migrateUpTo(ctx, "") }

// migrateUpTo applies pending migrations in order, stopping after `last` when set (tests).
func (s *SQL) migrateUpTo(ctx context.Context, last string) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		if last != "" && n > last {
			break
		}
		var v string
		err := s.db.QueryRowContext(ctx, s.rebind(`SELECT version FROM schema_migrations WHERE version=?`), n).Scan(&v)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		body, _ := migrationsFS.ReadFile("migrations/" + n)
		for _, stmt := range strings.Split(string(body), ";") {
			stmt = strings.TrimSpace(stripComments(stmt))
			if stmt == "" {
				continue
			}
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("%s: %w (%s)", n, err, stmt)
			}
		}
		if _, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO schema_migrations(version, applied_at) VALUES(?,?)`), n, FmtTime(time.Now())); err != nil {
			return err
		}
	}
	return nil
}

func stripComments(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "--") {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// rebind converts ? placeholders to $n for Postgres.
func (s *SQL) rebind(q string) string {
	if s.dialect != "postgres" {
		return q
	}
	var b strings.Builder
	n := 0
	for _, c := range q {
		if c == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// nullStr maps "" to SQL NULL (columns added by ALTER TABLE are nullable).
func nullStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func (s *SQL) exec(ctx context.Context, q string, args ...any) error {
	_, err := s.db.ExecContext(ctx, s.rebind(q), args...)
	return err
}

// ---- generic document helpers ----

// querier is *sql.DB or *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// putDoc upserts a row: id + extra columns + doc JSON.
func (s *SQL) putDoc(ctx context.Context, table, id string, createdAt time.Time, cols map[string]any, doc any) error {
	return s.putDocQ(ctx, s.db, table, id, createdAt, cols, doc)
}

// putDocQ is putDoc on a transaction (SQLite has one connection: inside a
// transaction every statement must go through the tx).
func (s *SQL) putDocQ(ctx context.Context, qr querier, table, id string, createdAt time.Time, cols map[string]any, doc any) error {
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	names := []string{"id", "created_at", "doc"}
	vals := []any{id, FmtTime(createdAt), string(b)}
	keys := make([]string, 0, len(cols))
	for k := range cols {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		names = append(names, k)
		vals = append(vals, cols[k])
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
	var sets []string
	for _, n := range names[1:] {
		sets = append(sets, n+"=excluded."+n)
	}
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT(id) DO UPDATE SET %s",
		table, strings.Join(names, ","), ph, strings.Join(sets, ","))
	_, err = qr.ExecContext(ctx, s.rebind(q), vals...)
	return err
}

func getDoc[T any](ctx context.Context, s *SQL, table, where string, args ...any) (*T, error) {
	return getDocQ[T](ctx, s, s.db, table, where, args...)
}

func getDocQ[T any](ctx context.Context, s *SQL, qr querier, table, where string, args ...any) (*T, error) {
	var raw string
	err := qr.QueryRowContext(ctx, s.rebind("SELECT doc FROM "+table+" WHERE "+where), args...).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	var v T
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func listDocs[T any](ctx context.Context, s *SQL, table, where, order string, args ...any) ([]*T, error) {
	q := "SELECT doc FROM " + table
	if where != "" {
		q += " WHERE " + where
	}
	if order != "" {
		q += " ORDER BY " + order
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*T{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		out = append(out, &v)
	}
	return out, rows.Err()
}

// docWithHidden wraps entities whose sensitive fields are tagged json:"-".
type hostDoc struct {
	model.Host
	TokenHash     string `json:"token_hash"`
	BootstrapHash string `json:"bootstrap_hash"`
}
type keyDoc struct {
	model.APIKey
	Hash string `json:"hash"`
}
type providerDoc struct {
	model.Provider
	TokenEnc string `json:"token_enc"`
}
type secretDoc struct {
	model.Secret
	ValueEnc string `json:"value_enc"`
}
type jobDoc struct {
	model.Job
	AgentID string `json:"agent_id"`
}
type approvalDoc struct {
	model.Approval
	DedupeKey string `json:"dedupe_key"`
}

// ---- agents ----

func (s *SQL) PutAgent(ctx context.Context, a *model.Agent) error {
	return s.putDoc(ctx, "agents", a.ID, a.CreatedAt, map[string]any{"name": a.Name, "workspace_id": a.WorkspaceID}, a)
}
func (s *SQL) GetAgent(ctx context.Context, id string) (*model.Agent, error) {
	return getDoc[model.Agent](ctx, s, "agents", "id=?", id)
}
func (s *SQL) GetAgentByName(ctx context.Context, name string) (*model.Agent, error) {
	return getDoc[model.Agent](ctx, s, "agents", "name=?", name)
}
func (s *SQL) ListAgents(ctx context.Context) ([]*model.Agent, error) {
	return listDocs[model.Agent](ctx, s, "agents", "", "created_at")
}
func (s *SQL) DeleteAgent(ctx context.Context, id string) error {
	return s.exec(ctx, "DELETE FROM agents WHERE id=?", id)
}

// ---- keys ----

func (s *SQL) PutKey(ctx context.Context, k *model.APIKey) error {
	agent := ""
	if k.AgentID != nil {
		agent = *k.AgentID
	}
	return s.putDoc(ctx, "api_keys", k.ID, k.CreatedAt, map[string]any{"agent_id": agent, "hash": k.Hash, "workspace_id": nullStr(k.WorkspaceID)}, keyDoc{*k, k.Hash})
}
func unKey(d *keyDoc, err error) (*model.APIKey, error) {
	if err != nil {
		return nil, err
	}
	k := d.APIKey
	k.Hash = d.Hash
	return &k, nil
}
func (s *SQL) GetKeyByHash(ctx context.Context, hash string) (*model.APIKey, error) {
	return unKey(getDoc[keyDoc](ctx, s, "api_keys", "hash=?", hash))
}
func (s *SQL) GetKey(ctx context.Context, id string) (*model.APIKey, error) {
	return unKey(getDoc[keyDoc](ctx, s, "api_keys", "id=?", id))
}
func (s *SQL) ListKeys(ctx context.Context, agentID string) ([]*model.APIKey, error) {
	where, args := "", []any{}
	if agentID != "" {
		where, args = "agent_id=?", []any{agentID}
	}
	ds, err := listDocs[keyDoc](ctx, s, "api_keys", where, "created_at", args...)
	if err != nil {
		return nil, err
	}
	out := make([]*model.APIKey, 0, len(ds))
	for _, d := range ds {
		k := d.APIKey
		out = append(out, &k)
	}
	return out, nil
}

// ---- boxes ----

func (s *SQL) PutBox(ctx context.Context, b *model.Box) error {
	return s.putDoc(ctx, "boxes", b.ID, b.CreatedAt, map[string]any{
		"agent_id": b.AgentID, "workspace_id": nullStr(b.WorkspaceID), "status": b.Status, "lifecycle": b.Lifecycle, "provider": b.Provider, "host_id": b.HostID,
	}, b)
}
func (s *SQL) GetBox(ctx context.Context, id string) (*model.Box, error) {
	b, err := getDoc[model.Box](ctx, s, "boxes", "id=?", id)
	if err == model.ErrNoRows {
		// allow lookup by slug
		b, err = getDoc[model.Box](ctx, s, "boxes", "id IN (SELECT id FROM boxes WHERE status<>'terminated') AND doc LIKE ?", "%\"slug\":\""+id+"\"%")
	}
	return b, err
}
func (s *SQL) ListBoxes(ctx context.Context, f BoxFilter) ([]*model.Box, error) {
	var conds []string
	var args []any
	add := func(c string, v any) { conds = append(conds, c); args = append(args, v) }
	if f.AgentID != "" {
		add("agent_id=?", f.AgentID)
	}
	if f.WorkspaceID != "" {
		add("workspace_id=?", f.WorkspaceID)
	}
	if f.Status != "" {
		add("status=?", f.Status)
	} else if !f.IncludeTerminated && len(f.Statuses) == 0 {
		conds = append(conds, "status NOT IN ('terminated','failed')")
	}
	if len(f.Statuses) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(f.Statuses)), ",")
		conds = append(conds, "status IN ("+ph+")")
		for _, st := range f.Statuses {
			args = append(args, st)
		}
	}
	if f.Provider != "" {
		add("provider=?", f.Provider)
	}
	if f.Lifecycle != "" {
		add("lifecycle=?", f.Lifecycle)
	}
	if f.HostID != "" {
		add("host_id=?", f.HostID)
	}
	return listDocs[model.Box](ctx, s, "boxes", strings.Join(conds, " AND "), "created_at DESC", args...)
}

// ---- hosts ----

func (s *SQL) PutHost(ctx context.Context, h *model.Host) error {
	return s.putDoc(ctx, "hosts", h.ID, h.CreatedAt, map[string]any{
		"provider": h.Provider, "status": h.Status, "token_hash": h.TokenHash, "bootstrap_hash": h.BootstrapHash,
	}, hostDoc{*h, h.TokenHash, h.BootstrapHash})
}
func unHost(d *hostDoc, err error) (*model.Host, error) {
	if err != nil {
		return nil, err
	}
	h := d.Host
	h.TokenHash, h.BootstrapHash = d.TokenHash, d.BootstrapHash
	return &h, nil
}
func (s *SQL) GetHost(ctx context.Context, id string) (*model.Host, error) {
	return unHost(getDoc[hostDoc](ctx, s, "hosts", "id=?", id))
}
func (s *SQL) GetHostByToken(ctx context.Context, hash string) (*model.Host, error) {
	return unHost(getDoc[hostDoc](ctx, s, "hosts", "token_hash=? AND token_hash<>''", hash))
}
func (s *SQL) GetHostByBootstrap(ctx context.Context, hash string) (*model.Host, error) {
	return unHost(getDoc[hostDoc](ctx, s, "hosts", "bootstrap_hash=? AND bootstrap_hash<>''", hash))
}
func (s *SQL) ListHosts(ctx context.Context) ([]*model.Host, error) {
	ds, err := listDocs[hostDoc](ctx, s, "hosts", "", "created_at")
	if err != nil {
		return nil, err
	}
	out := make([]*model.Host, 0, len(ds))
	for _, d := range ds {
		h := d.Host
		h.TokenHash, h.BootstrapHash = d.TokenHash, d.BootstrapHash
		out = append(out, &h)
	}
	return out, nil
}
func (s *SQL) DeleteHost(ctx context.Context, id string) error {
	return s.exec(ctx, "DELETE FROM hosts WHERE id=?", id)
}

// ---- providers ----

func (s *SQL) PutProvider(ctx context.Context, p *model.Provider) error {
	return s.putDoc(ctx, "providers", p.ID, time.Now(), nil, providerDoc{*p, p.TokenEnc})
}
func (s *SQL) GetProvider(ctx context.Context, id string) (*model.Provider, error) {
	d, err := getDoc[providerDoc](ctx, s, "providers", "id=?", id)
	if err != nil {
		return nil, err
	}
	p := d.Provider
	p.TokenEnc = d.TokenEnc
	return &p, nil
}
func (s *SQL) ListProviders(ctx context.Context) ([]*model.Provider, error) {
	ds, err := listDocs[providerDoc](ctx, s, "providers", "", "id")
	if err != nil {
		return nil, err
	}
	out := make([]*model.Provider, 0, len(ds))
	for _, d := range ds {
		p := d.Provider
		p.TokenEnc = d.TokenEnc
		out = append(out, &p)
	}
	return out, nil
}

// ---- offers & capabilities ----

// ReplaceOffers swaps all offers of (provider, source) and appends price history on change.
func (s *SQL) ReplaceOffers(ctx context.Context, provider, source string, offers []model.Offer) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old := map[string]model.Offer{}
	rows, err := tx.QueryContext(ctx, s.rebind("SELECT doc FROM offers WHERE provider=? AND source=?"), provider, source)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) == nil {
			var o model.Offer
			if json.Unmarshal([]byte(raw), &o) == nil {
				old[o.Key()] = o
			}
		}
	}
	rows.Close()
	if _, err := tx.ExecContext(ctx, s.rebind("DELETE FROM offers WHERE provider=? AND source=?"), provider, source); err != nil {
		return err
	}
	now := FmtTime(time.Now())
	seen := map[string]bool{}
	for _, o := range offers {
		k := o.Key()
		if seen[k] {
			continue
		}
		seen[k] = true
		b, _ := json.Marshal(o)
		if _, err := tx.ExecContext(ctx, s.rebind("INSERT INTO offers(id, provider, source, created_at, doc) VALUES(?,?,?,?,?)"), k, provider, source, now, string(b)); err != nil {
			return err
		}
		if prev, ok := old[k]; !ok || prev.PriceHour != o.PriceHour || prev.PriceMonth != o.PriceMonth {
			if _, err := tx.ExecContext(ctx, s.rebind("INSERT INTO price_history(id, offer_id, ts, price_hour, price_month) VALUES(?,?,?,?,?)"),
				fmt.Sprintf("ph_%d_%d", time.Now().UnixNano(), len(seen)), k, now, o.PriceHour, o.PriceMonth); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *SQL) ListOffers(ctx context.Context, provider string) ([]model.Offer, error) {
	where, args := "", []any{}
	if provider != "" {
		where, args = "provider=?", []any{provider}
	}
	ds, err := listDocs[model.Offer](ctx, s, "offers", where, "provider, id", args...)
	if err != nil {
		return nil, err
	}
	out := make([]model.Offer, 0, len(ds))
	for _, d := range ds {
		out = append(out, *d)
	}
	return out, nil
}

func (s *SQL) PutCapabilities(ctx context.Context, provider, source string, caps map[string]any) error {
	return s.putDoc(ctx, "capabilities", provider, time.Now(), map[string]any{"source": source}, caps)
}
func (s *SQL) GetCapabilities(ctx context.Context, provider string) (map[string]any, error) {
	m, err := getDoc[map[string]any](ctx, s, "capabilities", "id=?", provider)
	if err != nil {
		return nil, err
	}
	return *m, nil
}

// ---- jobs ----

func (s *SQL) PutJob(ctx context.Context, j *model.Job) error {
	return s.putDoc(ctx, "jobs", j.ID, j.CreatedAt, map[string]any{"box_id": j.BoxID, "status": j.Status}, jobDoc{*j, j.AgentID})
}
func (s *SQL) GetJob(ctx context.Context, id string) (*model.Job, error) {
	d, err := getDoc[jobDoc](ctx, s, "jobs", "id=?", id)
	if err != nil {
		return nil, err
	}
	j := d.Job
	j.AgentID = d.AgentID
	return &j, nil
}
func (s *SQL) ListJobs(ctx context.Context, boxID string, limit int) ([]*model.Job, error) {
	where, args := "", []any{}
	if boxID != "" {
		where, args = "box_id=?", []any{boxID}
	}
	order := "created_at DESC"
	if limit > 0 {
		order += fmt.Sprintf(" LIMIT %d", limit)
	}
	ds, err := listDocs[jobDoc](ctx, s, "jobs", where, order, args...)
	if err != nil {
		return nil, err
	}
	out := make([]*model.Job, 0, len(ds))
	for _, d := range ds {
		j := d.Job
		j.AgentID = d.AgentID
		out = append(out, &j)
	}
	return out, nil
}

// ---- events ----

func (s *SQL) AddEvent(ctx context.Context, e *model.Event) error {
	var data *string
	if len(e.Data) > 0 {
		b, _ := json.Marshal(e.Data)
		d := string(b)
		data = &d
	}
	return s.exec(ctx, "INSERT INTO events(id, ts, actor, box_id, agent_id, type, message, cost_usd, data, workspace_id) VALUES(?,?,?,?,?,?,?,?,?,?)",
		e.ID, FmtTime(e.TS), e.Actor, e.BoxID, e.AgentID, e.Type, e.Message, e.CostUSD, data, e.WorkspaceID)
}

func (s *SQL) ListEvents(ctx context.Context, f EventFilter) ([]*model.Event, error) {
	var conds []string
	var args []any
	add := func(c string, v any) { conds = append(conds, c); args = append(args, v) }
	if f.BoxID != "" {
		add("box_id=?", f.BoxID)
	}
	if f.AgentID != "" {
		add("agent_id=?", f.AgentID)
	}
	if f.Actor != "" {
		add("actor=?", f.Actor)
	}
	if f.Type != "" {
		add("type LIKE ?", f.Type+"%")
	}
	if f.WorkspaceID != "" {
		add("workspace_id=?", f.WorkspaceID)
	}
	q := "SELECT id, ts, actor, box_id, agent_id, type, message, cost_usd, data, workspace_id FROM events"
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q += fmt.Sprintf(" ORDER BY ts DESC LIMIT %d", limit)
	rows, err := s.db.QueryContext(ctx, s.rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.Event{}
	for rows.Next() {
		var e model.Event
		var ts string
		var box, agent, data, ws sql.NullString
		var cost sql.NullFloat64
		if err := rows.Scan(&e.ID, &ts, &e.Actor, &box, &agent, &e.Type, &e.Message, &cost, &data, &ws); err != nil {
			return nil, err
		}
		e.WorkspaceID = ws.String
		if data.Valid && data.String != "" {
			json.Unmarshal([]byte(data.String), &e.Data)
		}
		e.TS, _ = time.Parse(TimeFormat, ts)
		if box.Valid {
			e.BoxID = &box.String
		}
		if agent.Valid {
			e.AgentID = &agent.String
		}
		if cost.Valid {
			e.CostUSD = &cost.Float64
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// ---- ledger ----

func (s *SQL) AddLedger(ctx context.Context, l *model.LedgerEntry) error {
	return s.exec(ctx, "INSERT INTO ledger(id, ts, agent_id, box_id, provider, kind, billing, amount_usd, note, workspace_id) VALUES(?,?,?,?,?,?,?,?,?,?)",
		l.ID, FmtTime(l.TS), l.AgentID, l.BoxID, l.Provider, l.Kind, l.Billing, l.AmountUSD, l.Note, l.WorkspaceID)
}

func (s *SQL) ledgerWhere(f LedgerFilter) (string, []any) {
	var conds []string
	var args []any
	add := func(c string, v any) { conds = append(conds, c); args = append(args, v) }
	if f.AgentID != "" {
		add("agent_id=?", f.AgentID)
	}
	if f.BoxID != "" {
		add("box_id=?", f.BoxID)
	}
	if f.Provider != "" {
		add("provider=?", f.Provider)
	}
	if f.Billing != "" {
		add("billing=?", f.Billing)
	}
	if f.WorkspaceID != "" {
		add("workspace_id=?", f.WorkspaceID)
	}
	if !f.From.IsZero() {
		add("ts>=?", FmtTime(f.From))
	}
	if !f.To.IsZero() {
		add("ts<?", FmtTime(f.To))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (s *SQL) ListLedger(ctx context.Context, f LedgerFilter) ([]*model.LedgerEntry, error) {
	where, args := s.ledgerWhere(f)
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	q := fmt.Sprintf("SELECT id, ts, agent_id, box_id, provider, kind, billing, amount_usd, note, workspace_id FROM ledger%s ORDER BY ts DESC LIMIT %d", where, limit)
	rows, err := s.db.QueryContext(ctx, s.rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.LedgerEntry{}
	for rows.Next() {
		var l model.LedgerEntry
		var ts string
		var ws sql.NullString
		if err := rows.Scan(&l.ID, &ts, &l.AgentID, &l.BoxID, &l.Provider, &l.Kind, &l.Billing, &l.AmountUSD, &l.Note, &ws); err != nil {
			return nil, err
		}
		l.WorkspaceID = ws.String
		l.TS, _ = time.Parse(TimeFormat, ts)
		out = append(out, &l)
	}
	return out, rows.Err()
}

func (s *SQL) SumLedger(ctx context.Context, f LedgerFilter) (float64, error) {
	where, args := s.ledgerWhere(f)
	var v sql.NullFloat64
	err := s.db.QueryRowContext(ctx, s.rebind("SELECT SUM(amount_usd) FROM ledger"+where), args...).Scan(&v)
	return v.Float64, err
}

// SumLedgerBy groups by one of the whitelisted columns.
func (s *SQL) SumLedgerBy(ctx context.Context, f LedgerFilter, col string) (map[string]float64, error) {
	switch col {
	case "agent_id", "box_id", "provider", "billing", "kind":
	default:
		return nil, fmt.Errorf("bad group column %q", col)
	}
	where, args := s.ledgerWhere(f)
	rows, err := s.db.QueryContext(ctx, s.rebind("SELECT "+col+", SUM(amount_usd) FROM ledger"+where+" GROUP BY "+col), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var k string
		var v float64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (s *SQL) SumLedgerByDateAgent(ctx context.Context, from time.Time, workspaceID string) ([]DateAgentSum, error) {
	q, args := "SELECT substr(ts,1,10) AS d, agent_id, SUM(amount_usd) FROM ledger WHERE ts>=?", []any{FmtTime(from)}
	if workspaceID != "" {
		q, args = q+" AND workspace_id=?", append(args, workspaceID)
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(q+" GROUP BY substr(ts,1,10), agent_id ORDER BY d"), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DateAgentSum
	for rows.Next() {
		var d DateAgentSum
		if err := rows.Scan(&d.Date, &d.AgentID, &d.Amount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---- templates ----

func (s *SQL) PutTemplate(ctx context.Context, t *model.Template) error {
	return s.putDoc(ctx, "templates", ScopedID(t.WorkspaceID, t.Name), t.CreatedAt, map[string]any{"workspace_id": t.WorkspaceID}, t)
}

// ScopedID is the row id of a per-workspace named record (templates,
// secrets): the bare name for built-ins and the default workspace (rows
// written before v2.0 keep their ids), "<workspace>/<name>" otherwise.
func ScopedID(workspaceID, name string) string {
	if workspaceID == "" || workspaceID == model.DefaultWorkspaceID {
		return name
	}
	return workspaceID + "/" + name
}

// GetTemplateIn returns the workspace's template, else the built-in one.
func (s *SQL) GetTemplateIn(ctx context.Context, workspaceID, name string) (*model.Template, error) {
	if workspaceID != "" {
		if t, err := getDoc[model.Template](ctx, s, "templates", "id=? AND workspace_id=?", ScopedID(workspaceID, name), workspaceID); err == nil {
			return t, nil
		} else if !errors.Is(err, model.ErrNoRows) {
			return nil, err
		}
	}
	return getDoc[model.Template](ctx, s, "templates", "id=? AND (workspace_id IS NULL OR workspace_id='')", name)
}

// ListTemplatesIn lists built-ins plus the workspace's templates ("" = all rows).
func (s *SQL) ListTemplatesIn(ctx context.Context, workspaceID string) ([]*model.Template, error) {
	if workspaceID == "" {
		return s.ListTemplates(ctx)
	}
	all, err := listDocs[model.Template](ctx, s, "templates", "workspace_id IS NULL OR workspace_id='' OR workspace_id=?", "id", workspaceID)
	if err != nil {
		return nil, err
	}
	own := map[string]bool{}
	for _, t := range all {
		if t.WorkspaceID == workspaceID {
			own[t.Name] = true
		}
	}
	out := make([]*model.Template, 0, len(all))
	for _, t := range all {
		if t.WorkspaceID != workspaceID && own[t.Name] {
			continue // shadowed built-in
		}
		out = append(out, t)
	}
	return out, nil
}

func (s *SQL) DeleteTemplateIn(ctx context.Context, workspaceID, name string) error {
	return s.exec(ctx, "DELETE FROM templates WHERE id=? AND workspace_id=?", ScopedID(workspaceID, name), workspaceID)
}
func (s *SQL) GetTemplate(ctx context.Context, name string) (*model.Template, error) {
	return getDoc[model.Template](ctx, s, "templates", "id=?", name)
}
func (s *SQL) ListTemplates(ctx context.Context) ([]*model.Template, error) {
	return listDocs[model.Template](ctx, s, "templates", "", "id")
}
func (s *SQL) DeleteTemplate(ctx context.Context, name string) error {
	return s.exec(ctx, "DELETE FROM templates WHERE id=?", name)
}

// ---- secrets ----

func (s *SQL) PutSecret(ctx context.Context, sec *model.Secret) error {
	return s.putDoc(ctx, "secrets", ScopedID(sec.WorkspaceID, sec.Name), sec.CreatedAt, map[string]any{"workspace_id": nullStr(sec.WorkspaceID)}, secretDoc{*sec, sec.ValueEnc})
}

// GetSecretIn returns a workspace's secret by name.
func (s *SQL) GetSecretIn(ctx context.Context, workspaceID, name string) (*model.Secret, error) {
	d, err := getDoc[secretDoc](ctx, s, "secrets", "id=? AND workspace_id=?", ScopedID(workspaceID, name), workspaceID)
	if err != nil {
		return nil, err
	}
	sec := d.Secret
	sec.ValueEnc = d.ValueEnc
	return &sec, nil
}

func (s *SQL) DeleteSecretIn(ctx context.Context, workspaceID, name string) error {
	return s.exec(ctx, "DELETE FROM secrets WHERE id=? AND workspace_id=?", ScopedID(workspaceID, name), workspaceID)
}
func (s *SQL) GetSecret(ctx context.Context, name string) (*model.Secret, error) {
	d, err := getDoc[secretDoc](ctx, s, "secrets", "id=?", name)
	if err != nil {
		return nil, err
	}
	sec := d.Secret
	sec.ValueEnc = d.ValueEnc
	return &sec, nil
}
func (s *SQL) ListSecrets(ctx context.Context) ([]*model.Secret, error) {
	ds, err := listDocs[secretDoc](ctx, s, "secrets", "", "id")
	if err != nil {
		return nil, err
	}
	out := make([]*model.Secret, 0, len(ds))
	for _, d := range ds {
		sec := d.Secret
		out = append(out, &sec)
	}
	return out, nil
}
func (s *SQL) DeleteSecret(ctx context.Context, name string) error {
	return s.exec(ctx, "DELETE FROM secrets WHERE id=?", name)
}

// ---- snapshots ----

func (s *SQL) PutSnapshot(ctx context.Context, sn *model.Snapshot) error {
	return s.putDoc(ctx, "snapshots", sn.ID, sn.CreatedAt, map[string]any{"box_id": sn.BoxID}, sn)
}
func (s *SQL) ListSnapshots(ctx context.Context, boxID string) ([]*model.Snapshot, error) {
	where, args := "", []any{}
	if boxID != "" {
		where, args = "box_id=?", []any{boxID}
	}
	return listDocs[model.Snapshot](ctx, s, "snapshots", where, "created_at DESC", args...)
}
func (s *SQL) GetSnapshot(ctx context.Context, id string) (*model.Snapshot, error) {
	return getDoc[model.Snapshot](ctx, s, "snapshots", "id=?", id)
}
func (s *SQL) DeleteSnapshot(ctx context.Context, id string) error {
	return s.exec(ctx, "DELETE FROM snapshots WHERE id=?", id)
}

// ---- approvals ----

func (s *SQL) PutApproval(ctx context.Context, a *model.Approval) error {
	return s.putDoc(ctx, "approvals", a.ID, a.CreatedAt, map[string]any{"status": a.Status, "dedupe_key": a.DedupeKey, "workspace_id": a.WorkspaceID}, approvalDoc{*a, a.DedupeKey})
}
func unApproval(d *approvalDoc, err error) (*model.Approval, error) {
	if err != nil {
		return nil, err
	}
	a := d.Approval
	a.DedupeKey = d.DedupeKey
	return &a, nil
}
func (s *SQL) GetApproval(ctx context.Context, id string) (*model.Approval, error) {
	return unApproval(getDoc[approvalDoc](ctx, s, "approvals", "id=?", id))
}
func (s *SQL) FindApprovalByDedupe(ctx context.Context, key string) (*model.Approval, error) {
	return unApproval(getDoc[approvalDoc](ctx, s, "approvals", "dedupe_key=? ORDER BY created_at DESC LIMIT 1", key))
}
func (s *SQL) ListApprovals(ctx context.Context, status string) ([]*model.Approval, error) {
	return s.ListApprovalsIn(ctx, "", status)
}

func (s *SQL) ListApprovalsIn(ctx context.Context, workspaceID, status string) ([]*model.Approval, error) {
	var conds []string
	var args []any
	if status != "" {
		conds, args = append(conds, "status=?"), append(args, status)
	}
	if workspaceID != "" {
		conds, args = append(conds, "workspace_id=?"), append(args, workspaceID)
	}
	where := strings.Join(conds, " AND ")
	ds, err := listDocs[approvalDoc](ctx, s, "approvals", where, "created_at DESC", args...)
	if err != nil {
		return nil, err
	}
	out := make([]*model.Approval, 0, len(ds))
	for _, d := range ds {
		a := d.Approval
		out = append(out, &a)
	}
	return out, nil
}

// ---- idempotency, logs, kv ----

func (s *SQL) GetIdempotent(ctx context.Context, key string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, s.rebind("SELECT box_id FROM idempotency WHERE id=?"), key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", model.ErrNoRows
	}
	return id, err
}
func (s *SQL) PutIdempotent(ctx context.Context, key, boxID string) error {
	return s.exec(ctx, "INSERT INTO idempotency(id, box_id, created_at) VALUES(?,?,?)", key, boxID, FmtTime(time.Now()))
}
func (s *SQL) PutLog(ctx context.Context, id, boxID, hostID string) error {
	return s.exec(ctx, "INSERT INTO logs(id, box_id, host_id, created_at) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING", id, boxID, hostID, FmtTime(time.Now()))
}
func (s *SQL) GetLogHost(ctx context.Context, id string) (string, string, error) {
	var b, h string
	err := s.db.QueryRowContext(ctx, s.rebind("SELECT box_id, host_id FROM logs WHERE id=?"), id).Scan(&b, &h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", model.ErrNoRows
	}
	return b, h, err
}
func (s *SQL) GetKV(ctx context.Context, key string, out any) error {
	var raw string
	err := s.db.QueryRowContext(ctx, s.rebind("SELECT doc FROM kv WHERE id=?"), key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ErrNoRows
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), out)
}
func (s *SQL) PutKV(ctx context.Context, key string, v any) error {
	return s.putDoc(ctx, "kv", key, time.Now(), nil, v)
}

var _ Store = (*SQL)(nil)
