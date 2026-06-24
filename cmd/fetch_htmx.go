//go:build ignore

// fetch_htmx.go vendors HTMX into static/htmx.min.js. The vendored file is
// committed and embedded into the binary, so this is NOT part of the build —
// run it manually only when bumping the pinned HTMX version, then review and
// commit the resulting diff.
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
)

func main() {
	resp, err := http.Get("https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to download HTMX: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "HTTP %d downloading HTMX\n", resp.StatusCode)
		os.Exit(1)
	}

	f, err := os.Create("static/htmx.min.js")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create file: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write: %v\n", err)
		os.Exit(1)
	}
	if n < 20000 {
		os.Remove("static/htmx.min.js")
		fmt.Fprintf(os.Stderr, "Downloaded file too small (%d bytes) — likely corrupted\n", n)
		os.Exit(1)
	}
	fmt.Printf("Downloaded HTMX: %d bytes\n", n)
}
