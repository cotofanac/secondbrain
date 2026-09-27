package main

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

// Text assets are compressed once at startup; they are embedded, so they
// never change while the process runs.
var gzippedStatic = map[string][]byte{}

func compressStaticAssets(files fs.FS) error {
	return fs.WalkDir(files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !compressible(name) {
			return err
		}
		raw, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		zw.Write(raw)
		if err := zw.Close(); err != nil {
			return err
		}
		if buf.Len() < len(raw) {
			gzippedStatic[name] = buf.Bytes()
		}
		return nil
	})
}

func compressible(name string) bool {
	switch path.Ext(name) {
	case ".js", ".css", ".json", ".svg", ".html":
		return true
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// serveStatic serves the precompressed copy of an asset when the client takes
// gzip, and falls back to the plain file server otherwise.
func serveStatic(plain http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/static/")
		w.Header().Add("Vary", "Accept-Encoding")
		if gz, ok := gzippedStatic[name]; ok && acceptsGzip(r) {
			w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
			w.Header().Set("Content-Encoding", "gzip")
			http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(gz))
			return
		}
		plain.ServeHTTP(w, r)
	})
}

// writeHTML sends a rendered page, compressed when it is worth it.
func writeHTML(w http.ResponseWriter, r *http.Request, body []byte) {
	w.Header().Add("Vary", "Accept-Encoding")
	if r == nil || len(body) < 1024 || !acceptsGzip(r) {
		w.Write(body)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Encoding", "gzip")
	zw, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
	zw.Write(body)
	zw.Close()
}
