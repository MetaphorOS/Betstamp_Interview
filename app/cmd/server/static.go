package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web
var webFS embed.FS

// staticHandler serves the embedded page. The UI is built in a later step;
// until then web/index.html is a minimal live view of the SSE stream.
func staticHandler() http.Handler {
	sub, _ := fs.Sub(webFS, "web")
	return http.FileServerFS(sub)
}
