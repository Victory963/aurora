// Package auth verifies the HS256 access tokens issued by identity-svc (M2).
//
// NOTE: this duplicates identity-svc/internal/auth (the monorepo has no shared
// Go module yet). Shared auth lib + asymmetric keys: ADR-0005 §3 / M10.
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
	ErrInvalidToken = errors.New("invalid token")
	ErrExpiredToken = errors.New("token expired")
)

type Claims struct {
	Sub     string `json:"sub"`
	Sid     string `json:"sid"`
	Iss     string `json:"iss"`
	Iat     int64  `json:"iat"`
	Exp     int64  `json:"exp"`
	Country string `json:"country,omitempty"`
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

// Sign is used by tests (and kept for parity with identity); production room-svc
// only verifies.
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

// Verify checks the HS256 signature, alg, and expiry.
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
	// room-svc only accepts tokens issued by identity-svc. (identity's own
	// self-verification is intentionally lenient; this is the downstream consumer
	// being stricter — see ADR-0005 §3. Real fix is asymmetric keys in M10.)
	if c.Iss != Issuer {
		return nil, ErrInvalidToken
	}
	return &c, nil
}
