package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/hasanmahira/notifd/internal/domain"
)

func TestIssueVerifyRoundTrip(t *testing.T) {
	a := New("top-secret", "notifd")
	tok, err := a.Issue("mahir", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := a.Verify(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != "mahir" {
		t.Fatalf("sub: want mahir, got %s", claims.Subject)
	}
	if claims.Issuer != "notifd" {
		t.Fatalf("iss: want notifd, got %s", claims.Issuer)
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	a := New("top-secret", "notifd")
	tok, _ := a.Issue("mahir", time.Hour)
	b := New("different-secret", "notifd")
	_, err := b.Verify(tok)
	if err == nil || !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expected unauthorized, got %v", err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	a := New("s", "notifd")
	tok, _ := a.Issue("mahir", -time.Minute) // already expired
	_, err := a.Verify(tok)
	if err == nil || !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expected unauthorized, got %v", err)
	}
}
