package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed assets/*
var assets embed.FS

// Handler обслуживает панель HookForge без внешних зависимостей.
func Handler() http.Handler {
	public, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	index, err := fs.ReadFile(public, "index.html")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(public))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
			return
		} else if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/assets")
		} else {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}
