// Package static embeds the page's assets. mike.css is a committed copy, per the
// css repo's own rule: a product commits a copy and never edits it in place.
package static

import "embed"

//go:embed mike.css board.css theme.js highlight.js alive.js manifest.webmanifest icon.svg icon-192.png icon-512.png icon-512-maskable.png apple-touch-icon.png
var FS embed.FS
