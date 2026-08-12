package migrations

import "embed"

// Files contains the application schema migrations used by the bootstrap API.
//
//go:embed *.up.sql *.down.sql
var Files embed.FS
