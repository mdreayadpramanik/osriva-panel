// Package reqmeta carries per-request metadata (request ID, client IP, user
// agent) through context so services can audit without depending on HTTP.
package reqmeta

import "context"

type Meta struct {
	RequestID string
	IP        string
	UserAgent string
}

type ctxKey struct{}

func With(ctx context.Context, m Meta) context.Context { return context.WithValue(ctx, ctxKey{}, m) }

func From(ctx context.Context) Meta {
	m, _ := ctx.Value(ctxKey{}).(Meta)
	return m
}
