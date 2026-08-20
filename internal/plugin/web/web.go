// Package web embeds the built management UI (a single inlined index.html)
// and serves it as a CPA plugin resource under
// /v0/resource/plugins/access-guard/index.html.
//
// dist/index.html is a build artifact produced by `npm run build` in ../../web.
// A placeholder is committed so the Go build never fails when the frontend has
// not been built yet; the real UI replaces it after a frontend build.
package web

import (
	_ "embed"
	"net/http"
	"strings"
)

//go:embed dist/index.html
var indexHTML []byte

//go:embed logo.svg
var logoSVG []byte

const contentType = "text/html; charset=utf-8"

// IndexPath is the resource path (relative to the plugin resource base) the UI
// is served at.
const IndexPath = "/index.html"

// LogoPath is the resource path of the plugin logo shown in the panel's
// plugin list and menu.
const LogoPath = "/logo.svg"

// Serve returns a management response for a plugin resource GET request. It
// handles the index page and the logo; any other path yields 404.
func Serve(path string) (status int, headers http.Header, body []byte) {
	switch strings.TrimRight(path, "/") {
	case IndexPath:
		return http.StatusOK, http.Header{"Content-Type": []string{contentType}}, indexHTML
	case LogoPath:
		return http.StatusOK, http.Header{"Content-Type": []string{"image/svg+xml"}}, logoSVG
	}
	return http.StatusNotFound, http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}}, []byte("not found")
}
