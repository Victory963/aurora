package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword returns a bcrypt hash of the plaintext password.
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(b), err
}

// ComparePassword reports whether plain matches the stored bcrypt hash.
func ComparePassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// dummyHash is a fixed bcrypt hash used only to equalize timing.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("aurora-timing-equalizer"), bcrypt.DefaultCost)

// CompareDummy performs a throwaway bcrypt comparison so the unknown-user /
// no-credential login path spends the same CPU as a real password check,
// closing the user-enumeration timing side channel.
func CompareDummy(plain string) {
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(plain))
}

// NewRefreshToken mints a 256-bit random refresh token. It returns the opaque
// token (returned to the client exactly once) and its SHA-256 hex digest (what
// the DB stores — a leaked DB row cannot be replayed as a token).
func NewRefreshToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken is the SHA-256 hex digest used to look up / store refresh tokens.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
