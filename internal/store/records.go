package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Event is a stored event.
type Event struct {
	ID      int64           `json:"id"`
	TS      time.Time       `json:"ts"`
	Type    string          `json:"type"`
	Subject string          `json:"subject"`
	Payload json.RawMessage `json:"payload"`
}

// AddEvent stores an event and returns it with its ID.
func (s *Store) AddEvent(ctx context.Context, networkID int64, e Event) (Event, error) {
	if e.TS.IsZero() {
		e.TS = s.now()
	}
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage("{}")
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO events (network_id, ts, type, subject, payload_json) VALUES (?,?,?,?,?)`,
		networkID, e.TS.Unix(), e.Type, e.Subject, string(e.Payload))
	if err != nil {
		return Event{}, err
	}
	e.ID, _ = res.LastInsertId()
	return e, nil
}

// EventFilter narrows Events.
type EventFilter struct {
	Type  string // exact type or a prefix ending in "." (e.g. "client.")
	Since time.Time
	Limit int // default 200, max 1000
}

// Events lists events, newest first.
func (s *Store) Events(ctx context.Context, networkID int64, f EventFilter) ([]Event, error) {
	q := `SELECT id, ts, type, subject, payload_json FROM events WHERE network_id=? AND ts>=?`
	args := []any{networkID, f.Since.Unix()}
	switch {
	case strings.HasSuffix(f.Type, "."):
		q += ` AND type LIKE ? ESCAPE '\'`
		args = append(args, strings.NewReplacer(`%`, `\%`, `_`, `\_`).Replace(f.Type)+"%")
	case f.Type != "":
		q += ` AND type=?`
		args = append(args, f.Type)
	}
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}
	q += ` ORDER BY ts DESC, id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var ts int64
		var p string
		if err := rows.Scan(&e.ID, &ts, &e.Type, &e.Subject, &p); err != nil {
			return nil, err
		}
		e.TS, e.Payload = time.Unix(ts, 0), json.RawMessage(p)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Channel is a notification channel. ConfigEnc is sealed (it holds URLs
// and tokens) and is never returned by the API.
type Channel struct {
	ID          int64
	Kind        string // shoutrrr, openwa
	Name        string
	ConfigEnc   string
	Enabled     bool
	Events      []string // event types; empty = all
	WatchedOnly bool     // client events only for watched clients
	Template    string
}

func scanChannel(sc interface{ Scan(...any) error }) (Channel, error) {
	var c Channel
	var en, wo int
	var ev string
	err := sc.Scan(&c.ID, &c.Kind, &c.Name, &c.ConfigEnc, &en, &ev, &wo, &c.Template)
	c.Enabled, c.WatchedOnly = en == 1, wo == 1
	_ = json.Unmarshal([]byte(ev), &c.Events)
	return c, err
}

const channelCols = `id, kind, name, config_enc, enabled, events_json, watched_only, template`

// Channels lists all channels.
func (s *Store) Channels(ctx context.Context) ([]Channel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+channelCols+` FROM channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChannel returns one channel.
func (s *Store) GetChannel(ctx context.Context, id int64) (Channel, error) {
	return scanChannel(s.db.QueryRowContext(ctx, `SELECT `+channelCols+` FROM channels WHERE id=?`, id))
}

// SaveChannel inserts (ID 0) or updates a channel.
func (s *Store) SaveChannel(ctx context.Context, c Channel) (Channel, error) {
	ev, _ := json.Marshal(c.Events)
	if c.Events == nil {
		ev = []byte("[]")
	}
	if c.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO channels (kind, name, config_enc, enabled, events_json, watched_only, template) VALUES (?,?,?,?,?,?,?)`,
			c.Kind, c.Name, c.ConfigEnc, b2i(c.Enabled), string(ev), b2i(c.WatchedOnly), c.Template)
		if err != nil {
			return Channel{}, err
		}
		c.ID, _ = res.LastInsertId()
		return c, nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE channels SET kind=?, name=?, config_enc=?, enabled=?, events_json=?, watched_only=?, template=? WHERE id=?`,
		c.Kind, c.Name, c.ConfigEnc, b2i(c.Enabled), string(ev), b2i(c.WatchedOnly), c.Template, c.ID)
	if err != nil {
		return Channel{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Channel{}, ErrNotFound
	}
	return c, nil
}

// DeleteChannel removes a channel.
func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM channels WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Audit is one audit-log entry.
type Audit struct {
	ID                           int64     `json:"id"`
	TS                           time.Time `json:"ts"`
	User, Action, Target, Result string
	Error                        string `json:"error,omitempty"`
}

// AddAudit records an action. Never pass secrets in target or error.
func (s *Store) AddAudit(ctx context.Context, a Audit) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_log (ts, user, action, target, result, error) VALUES (?,?,?,?,?,?)`,
		s.now().Unix(), a.User, a.Action, a.Target, a.Result, a.Error)
	return err
}

// AuditLog lists the newest entries.
func (s *Store) AuditLog(ctx context.Context, limit int) ([]Audit, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, user, action, target, result, error FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Audit
	for rows.Next() {
		var a Audit
		var ts int64
		if err := rows.Scan(&a.ID, &ts, &a.User, &a.Action, &a.Target, &a.Result, &a.Error); err != nil {
			return nil, err
		}
		a.TS = time.Unix(ts, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetSetting decodes a stored setting into v; it reports false when unset.
func (s *Store) GetSetting(ctx context.Context, key string, v any) (bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key=?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), v)
}

// SetSetting stores v as JSON.
func (s *Store) SetSetting(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO settings (key, value_json) VALUES (?,?) ON CONFLICT (key) DO UPDATE SET value_json=excluded.value_json`, key, string(b))
	return err
}

// DeleteSetting removes a setting; a missing one is not an error.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, key)
	return err
}

// SetSettings saves several settings in one transaction: all or none.
func (s *Store) SetSettings(ctx context.Context, values map[string]any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range values {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value_json) VALUES (?,?) ON CONFLICT (key) DO UPDATE SET value_json=excluded.value_json`, k, string(b)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// User is a local account.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
}

// CountUsers reports how many accounts exist (0 = first-run setup).
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// ErrUserExists is returned when the first user was already created.
var ErrUserExists = errors.New("store: an account already exists")

// CreateFirstUser creates the admin account only when none exists, in one
// transaction, so first-run setup cannot be replayed.
func (s *Store) CreateFirstUser(ctx context.Context, username, hash string) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return User{}, err
	}
	if n > 0 {
		return User{}, ErrUserExists
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO users (username, password_hash, created_at) VALUES (?,?,?)`, username, hash, s.now().Unix())
	if err != nil {
		return User{}, err
	}
	id, _ := res.LastInsertId()
	return User{ID: id, Username: username, PasswordHash: hash}, tx.Commit()
}

// UserByName looks a user up.
func (s *Store) UserByName(ctx context.Context, username string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `SELECT id, username, password_hash FROM users WHERE username=?`, username).Scan(&u.ID, &u.Username, &u.PasswordHash)
	return u, err
}

// Session is a login session. Only the token's hash is stored.
type Session struct {
	TokenHash string
	UserID    int64
	Username  string
	CSRF      string
	ExpiresAt time.Time
}

// CreateSession stores a session.
func (s *Store) CreateSession(ctx context.Context, ss Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, csrf, created_at, expires_at) VALUES (?,?,?,?,?)`,
		ss.TokenHash, ss.UserID, ss.CSRF, s.now().Unix(), ss.ExpiresAt.Unix())
	return err
}

// GetSession returns an unexpired session.
func (s *Store) GetSession(ctx context.Context, tokenHash string) (Session, error) {
	var ss Session
	var exp int64
	err := s.db.QueryRowContext(ctx, `SELECT s.token_hash, s.user_id, u.username, s.csrf, s.expires_at FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=? AND s.expires_at>?`, tokenHash, s.now().Unix()).Scan(&ss.TokenHash, &ss.UserID, &ss.Username, &ss.CSRF, &exp)
	ss.ExpiresAt = time.Unix(exp, 0)
	return ss, err
}

// DeleteSession ends a session.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, tokenHash)
	return err
}
