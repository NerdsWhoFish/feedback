package auth

import (
	"context"
	"net/http"
)

// App assertions never confer browser privileges.
type Principal struct {
	UserID  string `json:"user_id,omitempty"`
	Email   string `json:"email,omitempty"`
	Name    string `json:"name,omitempty"`
	Role    string `json:"role,omitempty"`
	KeyID   string `json:"-"`
	KeyType string `json:"-"`
	AppName string `json:"-"`
	CSRF    string `json:"csrf_token,omitempty"`
}

func (p Principal) Admin() bool   { return p.KeyType == "" && p.Role == "admin" }
func (p Principal) Browser() bool { return p.KeyType == "" && p.UserID != "" }

type contextKey struct{}

func FromContext(ctx context.Context) Principal {
	p, _ := ctx.Value(contextKey{}).(Principal)
	return p
}
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

type Middleware interface {
	Require(http.Handler) http.Handler
}
