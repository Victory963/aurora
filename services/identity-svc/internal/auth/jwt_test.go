package auth

import (
	"testing"
	"time"
)

func TestSignVerify_RoundTrip(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Unix(1_700_000_000, 0)
	in := Claims{Sub: "u1", Sid: "s1", Iss: Issuer, Iat: now.Unix(), Exp: now.Add(15 * time.Minute).Unix(), Country: "GB"}

	tok, err := Sign(in, secret)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Verify(tok, secret, now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if out.Sub != "u1" || out.Sid != "s1" || out.Country != "GB" {
		t.Errorf("claims round-trip mismatch: %+v", out)
	}
}

func TestVerify_WrongSecret(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok, _ := Sign(Claims{Sub: "u1", Exp: now.Add(time.Minute).Unix()}, []byte("secret-a"))
	if _, err := Verify(tok, []byte("secret-b"), now); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestVerify_Expired(t *testing.T) {
	secret := []byte("s")
	issued := time.Unix(1_700_000_000, 0)
	tok, _ := Sign(Claims{Sub: "u1", Exp: issued.Add(15 * time.Minute).Unix()}, secret)
	later := issued.Add(16 * time.Minute)
	if _, err := Verify(tok, secret, later); err != ErrExpiredToken {
		t.Fatalf("expected ErrExpiredToken, got %v", err)
	}
}

func TestVerify_RejectsAlgNone(t *testing.T) {
	secret := []byte("s")
	// Forge a token with alg=none header and the same payload but an empty sig.
	none := b64([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + b64([]byte(`{"sub":"attacker"}`)) + "."
	if _, err := Verify(none, secret, time.Unix(1_700_000_000, 0)); err != ErrInvalidToken {
		t.Fatalf("alg=none must be rejected, got %v", err)
	}
}

func TestVerify_Malformed(t *testing.T) {
	if _, err := Verify("not-a-jwt", []byte("s"), time.Now()); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}
