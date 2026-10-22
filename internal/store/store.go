// Package store persists the code graph in SQLite.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

//go:embed schema.sql
var schemaV1 string

//go:embed schema_v2.sql
var schemaV2 string

// migrations[i] upgrades user_version i → i+1.
var migrations = []string{schemaV1, schemaV2}

// SchemaVersion is the user_version after all migrations.
var SchemaVersion = len(migrations)

// Options tune a connection.
type Options struct {
	ReadOnly      bool
	BusyTimeoutMS int // default 5000
}

// Store is an open index database.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (creating if needed) the database at path. The file is created
// 0600 so only the owner can read the index.
func Open(ctx context.Context, path string, opt Options) (*Store, error) {
	if opt.BusyTimeoutMS <= 0 {
		opt.BusyTimeoutMS = 5000
	}
	if !opt.ReadOnly {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, err
		}
		f.Close()
	} else if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("no index at %s (run `codegraph index`): %w", path, err)
	}

	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", opt.BusyTimeoutMS))
	q.Add("_pragma", "synchronous(normal)")
	q.Add("_pragma", "mmap_size(268435456)")
	q.Add("_pragma", "temp_store(memory)")
	q.Add("_pragma", "cache_size(-16000)")
	if opt.ReadOnly {
		q.Set("mode", "ro")
	} else {
		q.Add("_pragma", "journal_mode(wal)")
		q.Set("_txlock", "immediate")
	}
	dsn := (&url.URL{Scheme: "file", OmitHost: true, Path: filepath.ToSlash(path), RawQuery: q.Encode()}).String()
	db, err := driver.Open(dsn, func(c *sqlite3.Conn) error { return fts5.Register(c) })
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, path: path}
	if !opt.ReadOnly {
		// One writer connection: SQLite serialises writes anyway, and a single
		// connection keeps prepared statements and page cache warm.
		db.SetMaxOpenConns(1)
		if err := s.migrate(ctx); err != nil {
			db.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	var v int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > SchemaVersion {
		return fmt.Errorf("index schema v%d is newer than this binary (v%d); run `codegraph clean`", v, SchemaVersion)
	}
	for ; v < SchemaVersion; v++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// DB exposes the handle for read queries in other packages.
func (s *Store) DB() *sql.DB { return s.db }

// Optimize runs PRAGMA optimize; call after large writes.
func (s *Store) Optimize(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `PRAGMA optimize`)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

// Reset deletes all indexed data, keeping the schema.
func (s *Store) Reset(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []string{"files", "symbols", "edges", "refs", "meta", "resolve_dirty", "resolve_stats"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+t); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO symbols_fts(symbols_fts) VALUES('delete-all')`); err != nil {
		return err
	}
	return tx.Commit()
}

// SetMeta stores a key/value pair.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Meta returns a stored value, or "" when absent.
func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}
