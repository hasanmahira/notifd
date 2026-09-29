package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hasanmahira/notifd/internal/domain"
)

// Verifier validates incoming JWTs. Used by the auth middleware.
type Verifier interface {
	Verify(token string) (Claims, error)
}

// Issuer issues short-lived JWTs for demo/seed usage. Not for production.
type Issuer interface {
	Issue(sub string, ttl time.Duration) (string, error)
}

type Claims struct {
	Subject string
	Expiry  time.Time
	Issuer  string
}

// HS256 implements both Verifier and Issuer using HMAC-SHA256. For a production
// system we would back this with a JWKS asymmetric verifier.
type HS256 struct {
	secret []byte
	issuer string
}

func New(secret, issuer string) *HS256 {
	return &HS256{secret: []byte(secret), issuer: issuer}
}

func (h *HS256) Issue(subject string, ttl time.Duration) (string, error) {
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": h.issuer,
		"sub": subject,
		"iat": now.Unix(),
		"exp": now.Add(ttl).Unix(),
	})
	return tok.SignedString(h.secret)
}

func (h *HS256) Verify(tokenStr string) (Claims, error) {
	parsed, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return h.secret, nil
	},
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(h.issuer),     // fail if iss != configured issuer
		jwt.WithExpirationRequired(), // refuse tokens without exp
	)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %s", domain.ErrUnauthorized, err.Error())
	}
	if !parsed.Valid {
		return Claims{}, fmt.Errorf("%w: invalid token", domain.ErrUnauthorized)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return Claims{}, fmt.Errorf("%w: invalid claims", domain.ErrUnauthorized)
	}
	sub, _ := claims["sub"].(string)
	iss, _ := claims["iss"].(string)
	exp, _ := claims["exp"].(float64)
	if sub == "" {
		return Claims{}, fmt.Errorf("%w: missing sub", domain.ErrUnauthorized)
	}
	return Claims{Subject: sub, Expiry: time.Unix(int64(exp), 0), Issuer: iss}, nil
}

// ctx-key type for claims propagation.
type claimsKey struct{}

func WithClaims(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}

func FromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(claimsKey{}).(Claims)
	return c, ok
}

// Ensure compile error if signatures drift.
var _ = errors.New
