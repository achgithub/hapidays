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
	"hapidays/internal/seed"
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
	if err := st.SeedSmokeTestCollection(seed.SmokeTestCollection); err != nil {
		log.Printf("seed smoke-test collection: %v", err) // non-fatal — app still starts fine without it
	}

	uiFS, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("embed web assets: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", api.New(st))
	mux.Handle("/", http.FileServer(http.FS(uiFS)))

	addr := net.JoinHostPort(*host, fmt.Sprintf("%d", *port))
	log.Printf("hapidays listening on http://%s (data dir: %s)", addr, dir)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
