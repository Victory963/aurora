// Package auth implements identity-svc's M2 authentication primitives:
// HS256 JWT access tokens (hand-rolled on the stdlib, see ADR-0003 §3),
// bcrypt password hashing, and opaque refresh tokens.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const Issuer = "aurora-identity"

var (
	// ErrInvalidToken covers malformed tokens, bad signatures, and wrong alg.
	ErrInvalidToken = errors.New("invalid token")
	// ErrExpiredToken is returned when a structurally-valid token is past exp.
	ErrExpiredToken = errors.New("token expired")
)

// Claims are the access-token JWT claims.
type Claims struct {
	Sub     string `json:"sub"`               // user_id
	Sid     string `json:"sid"`               // session_id
	Iss     string `json:"iss"`               // always Issuer
	Iat     int64  `json:"iat"`               // issued-at unix seconds
	Exp     int64  `json:"exp"`               // expiry unix seconds
	Country string `json:"country,omitempty"` // kyc_country, convenience claim
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func mac(signingInput string, secret []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(signingInput))
	return m.Sum(nil)
}

// Sign produces an HS256 JWT for the given claims.
func Sign(claims Claims, secret []byte) (string, error) {
	h, err := json.Marshal(jwtHeader{Alg: "HS256", Typ: "JWT"})
	if err != nil {
		return "", err
	}
	p, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := b64(h) + "." + b64(p)
	return signingInput + "." + b64(mac(signingInput, secret)), nil
}

// Verify checks the HS256 signature, the alg header, and expiry, returning the
// claims. now is injected so tests are deterministic.
func Verify(token string, secret []byte, now time.Time) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}
	signingInput := parts[0] + "." + parts[1]

	gotSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrInvalidToken
	}
	// Constant-time comparison; also defeats alg=none since we always recompute HS256.
	if !hmac.Equal(mac(signingInput, secret), gotSig) {
		return nil, ErrInvalidToken
	}

	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalidToken
	}
	var hdr jwtHeader
	if err := json.Unmarshal(hb, &hdr); err != nil || hdr.Alg != "HS256" {
		return nil, ErrInvalidToken
	}

	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidToken
	}
	var c Claims
	if err := json.Unmarshal(pb, &c); err != nil {
		return nil, ErrInvalidToken
	}
	if c.Exp != 0 && !now.Before(time.Unix(c.Exp, 0)) {
		return nil, ErrExpiredToken
	}
	return &c, nil
}
