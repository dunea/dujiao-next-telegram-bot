// Package deployment embeds public installation defaults, never local secrets.
package deployment

import _ "embed"

// EnvExample is the same public template shipped in source and release bundles.
//
//go:embed .env.example
var EnvExample string
