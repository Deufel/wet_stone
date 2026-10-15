// Package static embeds the page's assets. mike.css is a committed copy, per the
// css repo's own rule: a product commits a copy and never edits it in place.
package static

import "embed"

//go:embed mike.css board.css
var FS embed.FS
