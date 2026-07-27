package auth

import (
	"testing"
	"time"
)

func TestSignVerify_RoundTrip(t *testing.T) {
	secret := []byte("s")
	now := time.Unix(1_700_000_000, 0)
	tok, err := Sign(Claims{Sub: "u1", Iss: Issuer, Iat: now.Unix(), Exp: now.Add(time.Hour).Unix()}, secret)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Verify(tok, secret, now)
	if err != nil || c.Sub != "u1" {
		t.Fatalf("verify: err=%v claims=%+v", err, c)
	}
}

func TestVerify_WrongSecret(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok, _ := Sign(Claims{Sub: "u1", Exp: now.Add(time.Minute).Unix()}, []byte("a"))
	if _, err := Verify(tok, []byte("b"), now); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestVerify_Expired(t *testing.T) {
	secret := []byte("s")
	issued := time.Unix(1_700_000_000, 0)
	tok, _ := Sign(Claims{Sub: "u1", Exp: issued.Add(time.Minute).Unix()}, secret)
	if _, err := Verify(tok, secret, issued.Add(2*time.Minute)); err != ErrExpiredToken {
		t.Fatalf("expected ErrExpiredToken, got %v", err)
	}
}

func TestVerify_WrongIssuer(t *testing.T) {
	secret := []byte("s")
	now := time.Unix(1_700_000_000, 0)
	// Validly signed, non-expired, but issued by someone other than identity-svc.
	tok, _ := Sign(Claims{Sub: "u1", Iss: "evil", Exp: now.Add(time.Hour).Unix()}, secret)
	if _, err := Verify(tok, secret, now); err != ErrInvalidToken {
		t.Fatalf("wrong issuer must be rejected, got %v", err)
	}
}

func TestVerify_RejectsAlgNone(t *testing.T) {
	none := b64([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + b64([]byte(`{"sub":"attacker"}`)) + "."
	if _, err := Verify(none, []byte("s"), time.Unix(1_700_000_000, 0)); err != ErrInvalidToken {
		t.Fatalf("alg=none must be rejected, got %v", err)
	}
}
