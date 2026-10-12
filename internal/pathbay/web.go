package pathbay

import (
	"embed"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed web/*
var webFiles embed.FS

func adminPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		apiError(w, 405, "use GET or HEAD")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/admin/")
	if name == "" {
		name = "index.html"
	}
	b, err := webFiles.ReadFile("web/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
	if r.Method != "HEAD" {
		w.Write(b)
	}
}
