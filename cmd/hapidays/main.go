// hapidays is a standalone, single-binary REST client — an open-source
// Postman alternative for environments where you can't install Postman
// (or anything else) yourself. It embeds its own UI and needs nothing on
// the target machine beyond the ability to run an executable.
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"hapidays/internal/api"
	"hapidays/internal/store"
)

//go:embed web
var webFS embed.FS

func main() {
	host := flag.String("host", "127.0.0.1", "address to bind (loopback by default — this server is not meant to be exposed on a network)")
	port := flag.Int("port", 8317, "port to listen on")
	dataDir := flag.String("data-dir", "", "directory to store collections/environments/history (default: alongside the binary, in ./hapidays-data)")
	flag.Parse()

	dir := *dataDir
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			log.Fatalf("resolve executable path: %v", err)
		}
		dir = filepath.Join(filepath.Dir(exe), "hapidays-data")
	}

	st, err := store.New(dir)
	if err != nil {
		log.Fatalf("init store at %s: %v", dir, err)
	}
	uiFS, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("embed web assets: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", api.New(st))
	// embed.FS reports a zero-value ModTime for every file (embedding
	// doesn't preserve real mtimes), so without an explicit no-store here,
	// http.FileServer's default caching behavior gives browsers nothing
	// reliable to revalidate against and some end up caching the UI far
	// more aggressively than a plain refresh should — the binary changes
	// on every rebuild, so there's never a good reason to reuse a cached
	// copy of these files.
	fileServer := http.FileServer(http.FS(uiFS))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		fileServer.ServeHTTP(w, r)
	}))

	addr := net.JoinHostPort(*host, fmt.Sprintf("%d", *port))
	log.Printf("hapidays listening on http://%s (data dir: %s)", addr, dir)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
