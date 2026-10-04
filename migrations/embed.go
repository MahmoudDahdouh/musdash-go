// Package migrations embeds the numbered SQL schema files.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
