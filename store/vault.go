package store

import (
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// Creds are encrypted with a random data key. The data key is stored
// encrypted with a key derived from the rem password by Argon2id, so
// changing the password only re-encrypts the data key. Everything is
// encrypted with XChaCha20-Poly1305, whose random 24 byte nonces are safe to
// pick without tracking which were used.

var (
	ErrWrongPassword = errors.New("incorrect rem password")
	ErrNoVault       = errors.New("no rem password set")
)

// kdfParams are Argon2id's costs. They're stored with the vault, so they
// can be raised later without breaking existing vaults.
type kdfParams struct {
	time    uint32 // passes over the memory
	memory  uint32 // KiB
	threads uint8
}

// RFC 9106's recommendation for memory-constrained use, about 0.1-0.3s
var defaultParams = kdfParams{time: 3, memory: 64 * 1024, threads: 4}

// Limits on params read back from the database, so an edited database
// can't make unlocking take forever or exhaust memory.
const (
	maxTime   = 100
	maxMemory = 4 * 1024 * 1024 // 4 GiB
)

const (
	saltSize = 16
	keySize  = chacha20poly1305.KeySize
)

// Additional data binds each ciphertext to what it is, so one can't be
// swapped in for another in the database.
const vaultAD = "rem vault key"

func credAD(name string) []byte {
	return []byte("rem cred " + name)
}

// Key is the unlocked data key.
type Key struct {
	data []byte
	aead cipher.AEAD
}

func newKey(data []byte) (*Key, error) {
	aead, err := chacha20poly1305.NewX(data)
	if err != nil {
		return nil, err
	}
	return &Key{data: data, aead: aead}, nil
}

// Reveal decrypts a cred's password.
func (k *Key) Reveal(c Cred) (string, error) {
	plain, err := open(k.aead, c.nonce, c.secret, credAD(c.Name))
	if err != nil {
		return "", fmt.Errorf("cred %q can't be decrypted, the database may be corrupt", c.Name)
	}
	return string(plain), nil
}

func (s *Store) HasVault() (bool, error) {
	var n int
	err := s.db.QueryRow("SELECT count(*) FROM vault").Scan(&n)
	return n > 0, err
}

// CreateVault sets the rem password, with a new data key.
func (s *Store) CreateVault(password string) (*Key, error) {
	data := make([]byte, keySize)
	rand.Read(data)
	key, err := newKey(data)
	if err != nil {
		return nil, err
	}

	salt, nonce, wrapped, err := wrap(data, password, defaultParams)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec("INSERT INTO vault (id, salt, time, memory, threads, nonce, wrapped_key) VALUES (1, ?, ?, ?, ?, ?, ?)",
		salt, defaultParams.time, defaultParams.memory, defaultParams.threads, nonce, wrapped)
	if isUniqueErr(err) {
		return nil, errors.New("a rem password is already set")
	}
	return key, err
}

// Unlock returns the data key, or ErrWrongPassword.
func (s *Store) Unlock(password string) (*Key, error) {
	var salt, nonce, wrapped []byte
	var p kdfParams
	err := s.db.QueryRow("SELECT salt, time, memory, threads, nonce, wrapped_key FROM vault WHERE id = 1").
		Scan(&salt, &p.time, &p.memory, &p.threads, &nonce, &wrapped)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoVault
	} else if err != nil {
		return nil, err
	}
	if p.time < 1 || p.time > maxTime || p.threads < 1 || p.memory < 8*uint32(p.threads) || p.memory > maxMemory {
		return nil, fmt.Errorf("vault has invalid Argon2 parameters (t=%d m=%dKiB p=%d)", p.time, p.memory, p.threads)
	}

	aead, err := chacha20poly1305.NewX(deriveKey(password, salt, p))
	if err != nil {
		return nil, err
	}
	data, err := open(aead, nonce, wrapped, []byte(vaultAD))
	if err != nil {
		// The only way a well-formed vault fails to open
		return nil, ErrWrongPassword
	}
	return newKey(data)
}

// ChangePassword re-encrypts the data key with a new rem password. Creds
// stay as they are.
func (s *Store) ChangePassword(key *Key, password string) error {
	salt, nonce, wrapped, err := wrap(key.data, password, defaultParams)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE vault SET salt = ?, time = ?, memory = ?, threads = ?, nonce = ?, wrapped_key = ? WHERE id = 1",
		salt, defaultParams.time, defaultParams.memory, defaultParams.threads, nonce, wrapped)
	return err
}

func deriveKey(password string, salt []byte, p kdfParams) []byte {
	return argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, keySize)
}

// wrap encrypts the data key with a key derived from password and a new
// salt.
func wrap(data []byte, password string, p kdfParams) (salt, nonce, wrapped []byte, err error) {
	salt = make([]byte, saltSize)
	rand.Read(salt)
	aead, err := chacha20poly1305.NewX(deriveKey(password, salt, p))
	if err != nil {
		return nil, nil, nil, err
	}
	nonce, wrapped = seal(aead, data, []byte(vaultAD))
	return salt, nonce, wrapped, nil
}

func seal(aead cipher.AEAD, plain, ad []byte) (nonce, sealed []byte) {
	nonce = make([]byte, aead.NonceSize())
	rand.Read(nonce)
	return nonce, aead.Seal(nil, nonce, plain, ad)
}

func open(aead cipher.AEAD, nonce, sealed, ad []byte) ([]byte, error) {
	if len(nonce) != aead.NonceSize() {
		return nil, errors.New("bad nonce")
	}
	return aead.Open(nil, nonce, sealed, ad)
}
