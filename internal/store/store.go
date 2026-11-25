// Package store keeps Atlantis state in SQLite (modernc.org/sqlite, no CGO):
// the site, its nodes and clients with history, events, notification
// channels, the audit log, settings, and local users and sessions.
// Migrations are embedded and applied in order on Open.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is the database. It is safe for concurrent use.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (creating if needed) the database at path and migrates it.
// Use ":memory:" for tests.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	if path == ":memory:" {
		dsn = "file::memory:?_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1) // one connection, or each gets its own empty database
	}
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// SetClock replaces the time source (tests).
func (s *Store) SetClock(now func() time.Time) { s.now = now }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&current); err != nil {
		return err
	}
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		base := strings.TrimPrefix(f, "migrations/")
		n, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("store: bad migration name %s", base)
		}
		if n <= current {
			continue
		}
		body, err := migrations.ReadFile(f)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %s: %w", base, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, n); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Network is the managed site.
type Network struct {
	ID          int64
	Name        string
	Host        string
	Transport   string
	Email       string
	PasswordEnc string // sealed with internal/secret; never returned by the API
	HostKey     string
	TLSPin      string
	CreatedAt   time.Time
}

// EnsureNetwork returns the site, creating it from n on first start. On
// later starts host, transport and email follow the configuration, and a
// non-empty sealed password replaces the stored one.
func (s *Store) EnsureNetwork(ctx context.Context, n Network) (Network, error) {
	var cur Network
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, host, transport, email, password_enc, host_key, tls_pin, created_at FROM networks ORDER BY id LIMIT 1`).
		Scan(&cur.ID, &cur.Name, &cur.Host, &cur.Transport, &cur.Email, &cur.PasswordEnc, &cur.HostKey, &cur.TLSPin, &created)
	switch {
	case err == sql.ErrNoRows:
		res, err := s.db.ExecContext(ctx, `INSERT INTO networks (name, host, transport, email, password_enc, host_key, tls_pin, created_at) VALUES (?,?,?,?,?,?,?,?)`,
			n.Name, n.Host, n.Transport, n.Email, n.PasswordEnc, n.HostKey, n.TLSPin, s.now().Unix())
		if err != nil {
			return Network{}, err
		}
		n.ID, _ = res.LastInsertId()
		n.CreatedAt = s.now()
		return n, nil
	case err != nil:
		return Network{}, err
	}
	cur.CreatedAt = time.Unix(created, 0)
	cur.Host, cur.Transport, cur.Email = n.Host, n.Transport, n.Email
	if n.PasswordEnc != "" {
		cur.PasswordEnc = n.PasswordEnc
	}
	if n.HostKey != "" {
		cur.HostKey = n.HostKey
	}
	if n.TLSPin != "" {
		cur.TLSPin = n.TLSPin
	}
	_, err = s.db.ExecContext(ctx, `UPDATE networks SET host=?, transport=?, email=?, password_enc=?, host_key=?, tls_pin=? WHERE id=?`,
		cur.Host, cur.Transport, cur.Email, cur.PasswordEnc, cur.HostKey, cur.TLSPin, cur.ID)
	return cur, err
}

// SetHostKey stores the SSH host key fingerprint to pin ("" clears it,
// which makes the next connection learn a new key).
func (s *Store) SetHostKey(ctx context.Context, networkID int64, fp string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE networks SET host_key=? WHERE id=?`, fp, networkID)
	return err
}

// SetTLSPin stores the TLS certificate fingerprint to pin ("" clears it).
func (s *Store) SetTLSPin(ctx context.Context, networkID int64, fp string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE networks SET tls_pin=? WHERE id=?`, fp, networkID)
	return err
}

// SetNetworkName records the site's display name.
func (s *Store) SetNetworkName(ctx context.Context, networkID int64, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE networks SET name=? WHERE id=?`, name, networkID)
	return err
}
