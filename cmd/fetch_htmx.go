//go:build ignore

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
