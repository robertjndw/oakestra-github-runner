// Package webui embeds the dashboard addon bundle into the binary.
//
// The bundle is built in ui/ and copied to dist/ here (see the Makefile and the Dockerfile).
// Only a placeholder is committed, so the module builds and tests run without a UI build; the
// server then simply has no UI files to serve.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the bundle rooted at its top level (mf-manifest.json, remoteEntry.js, assets/).
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// fs.Sub only fails for an invalid path, and "dist" is a constant.
		panic(err)
	}
	return sub
}
