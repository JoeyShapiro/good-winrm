package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	// Keep tests fast; the real costs are only about time
	old := defaultParams
	defaultParams = kdfParams{time: 1, memory: 64, threads: 1}
	t.Cleanup(func() { defaultParams = old })

	path := filepath.Join(t.TempDir(), "rem.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func addCred(t *testing.T, s *Store, key *Key, name, password string) Cred {
	t.Helper()
	if err := s.AddCred(Cred{Name: name, Username: "user-" + name, Password: password}, key); err != nil {
		t.Fatal(err)
	}
	c, err := s.Cred(name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCredRoundTrip(t *testing.T) {
	s, _ := openTest(t)
	key, err := s.CreateVault("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	addCred(t, s, key, "admin", "s3cret")

	// A fresh unlock, as a later run of rem would do
	key, err = s.Unlock("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Cred("admin")
	if err != nil {
		t.Fatal(err)
	}
	if c.Password != "" {
		t.Errorf("Cred returned a password without Reveal: %q", c.Password)
	}
	got, err := key.Reveal(c)
	if err != nil || got != "s3cret" {
		t.Errorf("Reveal = %q, %v; want s3cret", got, err)
	}
}

func TestWrongPassword(t *testing.T) {
	s, _ := openTest(t)
	if _, err := s.CreateVault("hunter2"); err != nil {
		t.Fatal(err)
	}
	for _, password := range []string{"hunter3", "", "Hunter2"} {
		if _, err := s.Unlock(password); !errors.Is(err, ErrWrongPassword) {
			t.Errorf("Unlock(%q) = %v, want ErrWrongPassword", password, err)
		}
	}
}

func TestNoVault(t *testing.T) {
	s, _ := openTest(t)
	if _, err := s.Unlock("anything"); !errors.Is(err, ErrNoVault) {
		t.Errorf("Unlock = %v, want ErrNoVault", err)
	}
	if _, err := s.CreateVault("one"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateVault("two"); err == nil {
		t.Error("second CreateVault succeeded")
	}
}

func TestChangePassword(t *testing.T) {
	s, _ := openTest(t)
	key, err := s.CreateVault("old")
	if err != nil {
		t.Fatal(err)
	}
	addCred(t, s, key, "admin", "s3cret")

	if err := s.ChangePassword(key, "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Unlock("old"); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("old password: %v, want ErrWrongPassword", err)
	}
	key, err = s.Unlock("new")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := s.Cred("admin")
	if got, err := key.Reveal(c); err != nil || got != "s3cret" {
		t.Errorf("after change, Reveal = %q, %v; want s3cret", got, err)
	}
}

// Encrypted passwords are bound to their cred's name, so editing the
// database to swap them doesn't work.
func TestSwappedCiphertext(t *testing.T) {
	s, _ := openTest(t)
	key, err := s.CreateVault("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	addCred(t, s, key, "admin", "admin-password")
	addCred(t, s, key, "guest", "guest-password")

	_, err = s.db.Exec(`UPDATE creds SET (nonce, password) =
		(SELECT nonce, password FROM creds WHERE name = 'admin') WHERE name = 'guest'`)
	if err != nil {
		t.Fatal(err)
	}
	guest, _ := s.Cred("guest")
	if got, err := key.Reveal(guest); err == nil {
		t.Errorf("Reveal of swapped cred = %q, want an error", got)
	}
}

func TestTamperedParams(t *testing.T) {
	s, _ := openTest(t)
	if _, err := s.CreateVault("hunter2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE vault SET memory = ?", maxMemory+1); err != nil {
		t.Fatal(err)
	}
	_, err := s.Unlock("hunter2")
	if err == nil || errors.Is(err, ErrWrongPassword) {
		t.Errorf("Unlock with huge memory = %v, want an invalid parameters error", err)
	}
}

func TestPasswordNotInFile(t *testing.T) {
	s, path := openTest(t)
	key, err := s.CreateVault("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	addCred(t, s, key, "admin", "plaintext-canary")
	s.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"plaintext-canary", "hunter2"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("database file contains %q", secret)
		}
	}
}
