// Package webui is the dashboard, built by `pnpm build` into dist/ and compiled into the binary.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS is the built dashboard, or nil when the binary was compiled without one.
func FS() fs.FS {
	sub, _ := fs.Sub(dist, "dist")
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
