// Package secret seals values at rest with AES-256-GCM and generates random
// identifiers and tokens.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// KeySize is the master key length in bytes.
const KeySize = 32

// ErrOpen is returned when a sealed value cannot be authenticated, whether
// because it was tampered with, truncated, or sealed with another key.
var ErrOpen = errors.New("secret: cannot open sealed value")

// Box seals and opens values with one key.
type Box struct {
	aead cipher.AEAD
}

// New returns a Box for a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secret: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plain and returns base64(nonce | ciphertext).
func (b *Box) Seal(plain []byte) (string, error) {
	nonce := make([]byte, b.aead.NonceSize(), b.aead.NonceSize()+len(plain)+b.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(b.aead.Seal(nonce, nonce, plain, nil)), nil
}

// SealString is Seal for a string value.
func (b *Box) SealString(plain string) (string, error) {
	return b.Seal([]byte(plain))
}

// Open reverses Seal.
func (b *Box) Open(sealed string) ([]byte, error) {
	raw, err := base64.RawStdEncoding.DecodeString(sealed)
	if err != nil || len(raw) < b.aead.NonceSize() {
		return nil, ErrOpen
	}
	nonce, ct := raw[:b.aead.NonceSize()], raw[b.aead.NonceSize():]
	plain, err := b.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}

// OpenString is Open returning a string. An empty input opens to "", so
// optional columns need no special casing.
func (b *Box) OpenString(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	plain, err := b.Open(sealed)
	return string(plain), err
}

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// RandomID returns a 12-character identifier of [a-z2-7] that starts with a
// letter, so it is safe as a URL segment, a container name and a DNS label.
func RandomID() string {
	b := RandomBytes(8)
	id := []byte(idEncoding.EncodeToString(b)[:12])
	if id[0] >= '2' && id[0] <= '7' {
		id[0] = 'a' + (id[0] - '2')
	}
	return string(id)
}

// RandomToken returns n random bytes as URL-safe base64.
func RandomToken(n int) string {
	return base64.RawURLEncoding.EncodeToString(RandomBytes(n))
}

// RandomHex returns n random bytes as lowercase hex.
func RandomHex(n int) string {
	return hex.EncodeToString(RandomBytes(n))
}

const alnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// RandomAlnum returns n random characters of [a-zA-Z0-9], for generated
// passwords that must survive being placed in URLs and shell commands.
func RandomAlnum(n int) string {
	out := make([]byte, n)
	// 248 is the largest multiple of 62 below 256; rejecting above it keeps
	// the distribution uniform.
	for i := 0; i < n; {
		for _, c := range RandomBytes(n - i + 8) {
			if c < 248 && i < n {
				out[i] = alnum[int(c)%len(alnum)]
				i++
			}
		}
	}
	return string(out)
}

// RandomBytes returns n bytes from the system's secure random source.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail on supported platforms; without
		// randomness nothing here is safe to continue.
		panic("secret: no randomness: " + err.Error())
	}
	return b
}

// HashToken returns the hex SHA-256 of a token. Session and API tokens are
// stored this way so a database leak does not expose usable tokens.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
