# ADR-0002: Technology stack and dependencies

* Status: Accepted (2026-11-02)

## Decision

| Concern | Choice | Why |
|---------|--------|-----|
| Backend language | Go | Single static binaries for the API and the agent, good cross-compilation to Linux/Windows/BSD and amd64/arm64, strong stdlib for crypto and HTTP. |
| HTTP router | `github.com/go-chi/chi/v5` | Small, stdlib-compatible `http.Handler`, maintained, no framework lock-in. |
| PostgreSQL driver | `github.com/jackc/pgx/v5` | De-facto standard, maintained, native types, pooling. |
| Password hashing | `golang.org/x/crypto/argon2` | Argon2id is the OWASP-recommended default. |
| Rate limiting | `golang.org/x/time/rate` | Token bucket from the Go project. |
| TOTP | stdlib (`crypto/hmac`, `crypto/sha1`) | ~50 lines against RFC 6238 test vectors, no dependency needed. |
| Logging | stdlib `log/slog` | Structured, no dependency. |
| Migrations | in-house, about 80 lines, embedded SQL | Forward-only, advisory-locked, transactional. Avoids pulling a large migration framework for a simple job. |
| Frontend | Next.js (App Router), React, TypeScript, Tailwind CSS v4 | As specified. `output: "standalone"` for small production images. |
| Monorepo tooling | npm workspaces + one Go module | pnpm/turbo add little at this size. Revisit when `packages/*` exist. |
| Reverse proxy | Caddy | Automatic HTTPS via ACME, simple config, Apache-2.0. |
| Database | PostgreSQL 16+ | Partial unique indexes, `inet`, `jsonb`, identity columns. |

Redis is deliberately **not** used in Phase 1, because nothing needs it yet. It will be
introduced with the background worker or distributed rate limiting.

## Consequences

* All dependencies are OSI-licensed (MIT/BSD-3/Apache-2.0) and compatible with AGPL-3.0.
* The Go module path `github.com/osriva/osriva-panel` is a placeholder until the
  public repository location is decided.
