package database

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// secretKeyEnv lets deployments inject the master key from the environment
// (for example through systemd's EnvironmentFile) instead of a file.
const secretKeyEnv = "AIRCOINS_SECRET_KEY"

// secretKeySize is the AES-256 key length in bytes.
const secretKeySize = 32

// secretPrefix marks the on-disk credential format so the cipher can be
// rotated later without guessing what a stored value means.
const secretPrefix = "v1:"

// secretBox encrypts RouterOS API passwords before they are written to SQLite.
// A stolen database file is then useless without the master key.
type secretBox struct {
	aead cipher.AEAD
}

func newSecretBox(key []byte) (*secretBox, error) {
	if len(key) != secretKeySize {
		return nil, fmt.Errorf("database: secret key must be %d bytes, got %d", secretKeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("database: create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("database: create gcm: %w", err)
	}
	return &secretBox{aead: aead}, nil
}

// Seal encrypts a credential. Empty input stays empty so rows without a
// password do not look encrypted.
func (b *secretBox) Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("database: nonce: %w", err)
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return secretPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a credential written by Seal.
func (b *secretBox) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if !strings.HasPrefix(sealed, secretPrefix) {
		return "", errors.New("database: unsupported credential format (was it written by an older key?)")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, secretPrefix))
	if err != nil {
		return "", fmt.Errorf("database: decode credential: %w", err)
	}
	if len(raw) < b.aead.NonceSize() {
		return "", errors.New("database: credential blob is truncated")
	}
	nonce, ciphertext := raw[:b.aead.NonceSize()], raw[b.aead.NonceSize():]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", errors.New("database: cannot decrypt credential: wrong master key?")
	}
	return string(plaintext), nil
}

// loadOrCreateSecretKey resolves the master key from, in priority order, the
// explicit config value, the environment and the key file. A missing key file
// is generated with 0600 permissions because losing it means losing every
// stored router password.
func loadOrCreateSecretKey(cfg Config) ([]byte, error) {
	if v := strings.TrimSpace(cfg.SecretKey); v != "" {
		key, err := decodeSecretKey(v)
		if err != nil {
			return nil, fmt.Errorf("database: invalid SecretKey: %w", err)
		}
		return key, nil
	}
	if v := strings.TrimSpace(os.Getenv(secretKeyEnv)); v != "" {
		key, err := decodeSecretKey(v)
		if err != nil {
			return nil, fmt.Errorf("database: invalid %s: %w", secretKeyEnv, err)
		}
		return key, nil
	}
	path := strings.TrimSpace(cfg.SecretKeyPath)
	if path == "" {
		return nil, fmt.Errorf("database: no master key configured (set SecretKeyPath or %s)", secretKeyEnv)
	}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, decErr := decodeSecretKey(string(data))
		if decErr != nil {
			return nil, fmt.Errorf("database: invalid master key in %s: %w", path, decErr)
		}
		return key, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("database: read master key %s: %w", path, err)
	}

	key := make([]byte, secretKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("database: generate master key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(key) + "\n"
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		return nil, fmt.Errorf("database: write master key %s: %w", path, err)
	}
	if cfg.Logger != nil {
		cfg.Logger.Info("generated new credential master key", "path", path)
	}
	return key, nil
}

// decodeSecretKey accepts base64 (standard or raw) and hex encoded keys so
// operators can paste whatever their secret manager produced.
func decodeSecretKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("empty key")
	}
	if b, err := base64.StdEncoding.DecodeString(value); err == nil && len(b) == secretKeySize {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(value); err == nil && len(b) == secretKeySize {
		return b, nil
	}
	if b, err := hex.DecodeString(value); err == nil && len(b) == secretKeySize {
		return b, nil
	}
	return nil, fmt.Errorf("key must be %d bytes as base64 or %d hex characters", secretKeySize, secretKeySize*2)
}
