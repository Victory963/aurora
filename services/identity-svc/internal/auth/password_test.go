package auth

import "testing"

func TestPasswordHashAndCompare(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "correct horse battery staple" {
		t.Fatal("password stored in plaintext")
	}
	if !ComparePassword(hash, "correct horse battery staple") {
		t.Error("correct password rejected")
	}
	if ComparePassword(hash, "wrong password") {
		t.Error("wrong password accepted")
	}
}

func TestRefreshTokenIsRandomAndHashed(t *testing.T) {
	t1, h1, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	t2, h2, _ := NewRefreshToken()
	if t1 == t2 {
		t.Error("refresh tokens must be unique")
	}
	if h1 == t1 {
		t.Error("stored hash must differ from the token")
	}
	if HashToken(t1) != h1 || HashToken(t2) != h2 {
		t.Error("HashToken must be deterministic for a given token")
	}
	if h1 == h2 {
		t.Error("distinct tokens must hash differently")
	}
}
