package migrations

import "embed"

// FS contains all database migrations compiled into the application binary.
//
//go:embed *.sql
var FS embed.FS
