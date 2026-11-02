// Package migrations embeds the versioned SQL migrations into the API binary.
package migrations

import "embed"

// FS holds every *.sql migration, applied in lexical order.
//
//go:embed *.sql
var FS embed.FS
