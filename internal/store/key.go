package store

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const keySize = 32

// LoadOrCreateKey returns the AES-256 key for the database. A configured envValue wins and is
// never written to disk. Otherwise the key lives in <dataDir>/secret.key and is generated on the
// first run. An existing key file that cannot be used is an error: generating a new key would
// silently orphan every stored secret.
func LoadOrCreateKey(dataDir, envValue string) ([]byte, error) {
	if v := strings.TrimSpace(envValue); v != "" {
		return decodeKey(v)
	}

	path := filepath.Join(dataDir, "secret.key")
	key, err := readKeyFile(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	key = make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		// Another process created it between our read and create; use theirs.
		return readKeyFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("create key file %s: %w", path, err)
	}
	_, werr := f.Write(key)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		// A truncated key file would be rejected on the next start, so do not leave one behind.
		_ = os.Remove(path)
		return nil, fmt.Errorf("write key file %s: %w", path, errors.Join(werr, cerr))
	}
	return key, nil
}

func readKeyFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("read key file %s: %w", path, err)
	}
	if len(b) != keySize {
		return nil, fmt.Errorf("key file %s has %d bytes, want %d; refusing to replace it", path, len(b), keySize)
	}
	return bytes.Clone(b), nil
}

// decodeKey accepts base64 or hex. A 64 character hex string is also valid base64 but decodes to
// 48 bytes, so trying both and checking the length is unambiguous.
func decodeKey(v string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(v); err == nil && len(b) == keySize {
		return b, nil
	}
	if b, err := hex.DecodeString(v); err == nil && len(b) == keySize {
		return b, nil
	}
	return nil, fmt.Errorf("encryption key must be %d bytes encoded as base64 or hex", keySize)
}
