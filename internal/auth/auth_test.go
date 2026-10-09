package auth

import (
	"testing"
	"time"
)

// P-AUTH-1: admin tokens must use the short TTL; non-admin keeps the long one.
func TestAdminTokenTTL(t *testing.T) {
	_, exp, err := GenerateToken("secret", 1, "admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(exp); d > AdminTokenTTL+time.Minute {
		t.Fatalf("admin token lifetime exceeds AdminTokenTTL: %v", d)
	}
	_, exp2, err := GenerateToken("secret", 2, "user", 0)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(exp2); d < TokenTTL-time.Minute {
		t.Fatalf("non-admin token lifetime below TokenTTL: %v", d)
	}
}

// P-AUTH-1: tokens carry jti and iat so revocation and password-change
// invalidation have something to key on.
func TestTokenCarriesJTIAndIAT(t *testing.T) {
	tok, _, err := GenerateToken("secret", 7, "admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ParseToken("secret", tok)
	if err != nil {
		t.Fatal(err)
	}
	if claims.ID == "" {
		t.Fatal("token missing jti")
	}
	if claims.IssuedAt == nil || claims.IssuedAt.IsZero() {
		t.Fatal("token missing iat")
	}
	// Second token gets a different jti: revoking one must not revoke the other.
	tok2, _, _ := GenerateToken("secret", 7, "admin", 0)
	claims2, _ := ParseToken("secret", tok2)
	if claims.ID == claims2.ID {
		t.Fatal("jti not unique per token")
	}
}

func TestRevocationListLifecycle(t *testing.T) {
	r := NewRevocationList()
	defer r.Close()
	if r.IsRevoked("jti-1") {
		t.Fatal("empty list revoked a token")
	}
	r.Revoke("jti-1", time.Now().Add(time.Hour))
	if !r.IsRevoked("jti-1") {
		t.Fatal("revocation not visible")
	}
	// Extending is allowed, shortening is not.
	r.Revoke("jti-1", time.Now().Add(time.Minute))
	if !r.IsRevoked("jti-1") {
		t.Fatal("revocation shortened away")
	}
	// Empty jti is a no-op.
	r.Revoke("", time.Now().Add(time.Hour))
}
