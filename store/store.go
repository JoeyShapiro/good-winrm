// Package store keeps saved targets and credentials in a SQLite database.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// Target is a machine to connect to over the network, the way to reach it,
// and the port. Credentials are kept separately, since one target can be
// logged into with many and many targets can share one.
type Target struct {
	Name    string
	Type    string // winrm or ssh
	Address string // host name or IP
	Port    int    // 0 for the type's default
}

// Cred is a username and password.
type Cred struct {
	Name     string
	Username string
	Password string
}

var ErrNotFound = errors.New("not found")

// Types are the target types the store accepts. Serial devices aren't
// saved, since many devices end up on the same port.
var Types = []string{"winrm", "ssh"}

// Bump with a new entry in migrations whenever the schema changes.
var migrations = []string{
	`CREATE TABLE targets (
		name    TEXT PRIMARY KEY,
		type    TEXT NOT NULL CHECK (type IN ('winrm', 'ssh')),
		address TEXT NOT NULL,
		port    INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE creds (
		name     TEXT PRIMARY KEY,
		username TEXT NOT NULL,
		password TEXT NOT NULL
	);`,
}

type Store struct {
	db *sql.DB
}

// DefaultPath is $REM_DB, or rem.db in the OS's config directory:
// ~/Library/Application Support/rem on macOS, $XDG_CONFIG_HOME/rem (usually
// ~/.config/rem) on Linux, and %AppData%\rem on Windows.
func DefaultPath() (string, error) {
	if path := os.Getenv("REM_DB"); path != "" {
		return path, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rem", "rem.db"), nil
}

// Open opens the store at path, creating it if needed.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	// SQLite would create the file world-readable, and it holds passwords
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()

	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("database is from a newer version of rem (schema %d)", version)
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) AddTarget(t Target) error {
	_, err := s.db.Exec("INSERT INTO targets (name, type, address, port) VALUES (?, ?, ?, ?)",
		t.Name, t.Type, t.Address, t.Port)
	if isUniqueErr(err) {
		return fmt.Errorf("target %q already exists", t.Name)
	}
	return err
}

func (s *Store) AddCred(c Cred) error {
	_, err := s.db.Exec("INSERT INTO creds (name, username, password) VALUES (?, ?, ?)",
		c.Name, c.Username, c.Password)
	if isUniqueErr(err) {
		return fmt.Errorf("cred %q already exists", c.Name)
	}
	return err
}

func (s *Store) Target(name string) (Target, error) {
	var t Target
	err := s.db.QueryRow("SELECT name, type, address, port FROM targets WHERE name = ?", name).
		Scan(&t.Name, &t.Type, &t.Address, &t.Port)
	if errors.Is(err, sql.ErrNoRows) {
		return t, fmt.Errorf("target %q: %w", name, ErrNotFound)
	}
	return t, err
}

func (s *Store) Cred(name string) (Cred, error) {
	var c Cred
	err := s.db.QueryRow("SELECT name, username, password FROM creds WHERE name = ?", name).
		Scan(&c.Name, &c.Username, &c.Password)
	if errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("cred %q: %w", name, ErrNotFound)
	}
	return c, err
}

func (s *Store) Targets() ([]Target, error) {
	rows, err := s.db.Query("SELECT name, type, address, port FROM targets ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var targets []Target
	for rows.Next() {
		var t Target
		if err := rows.Scan(&t.Name, &t.Type, &t.Address, &t.Port); err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

func (s *Store) Creds() ([]Cred, error) {
	rows, err := s.db.Query("SELECT name, username, password FROM creds ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var creds []Cred
	for rows.Next() {
		var c Cred
		if err := rows.Scan(&c.Name, &c.Username, &c.Password); err != nil {
			return nil, err
		}
		creds = append(creds, c)
	}
	return creds, rows.Err()
}

func (s *Store) RemoveTarget(name string) error {
	res, err := s.db.Exec("DELETE FROM targets WHERE name = ?", name)
	if err != nil {
		return err
	}
	return requireRow(res, "target", name)
}

func (s *Store) RemoveCred(name string) error {
	res, err := s.db.Exec("DELETE FROM creds WHERE name = ?", name)
	if err != nil {
		return err
	}
	return requireRow(res, "cred", name)
}

func requireRow(res sql.Result, kind, name string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s %q: %w", kind, name, ErrNotFound)
	}
	return nil
}

func isUniqueErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
