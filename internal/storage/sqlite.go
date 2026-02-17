package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// OpenSQLite opens (and creates if needed) the SQLite database at path and
// ensures required tables exist.
func OpenSQLite(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pragmas := []string{
		"PRAGMA foreign_keys = ON;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
	}
	for _, p := range pragmas {
		if _, err := db.ExecContext(pctx, p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("apply pragma %q: %w", p, err)
		}
	}

	db.SetMaxOpenConns(1)

	if err := bootstrap(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func bootstrap(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS runs (
			id           TEXT PRIMARY KEY,
			wake_id      TEXT UNIQUE,
			goal         TEXT NOT NULL,
			context      JSON,
			constraints  JSON,
			status       TEXT NOT NULL DEFAULT 'queued',
			summary      TEXT,
			error        TEXT,
			started_at   TEXT,
			completed_at TEXT,
			updated_at   TEXT NOT NULL,
			created_at   TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS steps (
			id           TEXT PRIMARY KEY,
			run_id       TEXT NOT NULL REFERENCES runs(id),
			step_num     INTEGER NOT NULL,
			phase        TEXT NOT NULL,
			tool         TEXT,
			tool_input   JSON,
			tool_output  JSON,
			status       TEXT NOT NULL,
			attempt      INTEGER NOT NULL DEFAULT 1,
			error        TEXT,
			started_at   TEXT,
			completed_at TEXT,
			created_at   TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS steps_run_id_idx ON steps(run_id, step_num);`,
	}

	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("bootstrap sqlite: %w", err)
		}
	}
	return nil
}
