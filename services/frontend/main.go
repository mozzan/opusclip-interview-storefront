// Command frontend serves the storefront page and proxies /api/backend/* and /api/payment/* to the
// other services, so the browser talks to one origin and the card number goes straight to the payment service.
package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"pinecone/internal/httpx"
)

//go:embed static
var static embed.FS

func proxy(prefix, target string) http.Handler {
	u, err := url.Parse(target)
	if err != nil {
		log.Fatalf("bad URL %q: %v", target, err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	return http.StripPrefix(strings.TrimSuffix(prefix, "/"), rp)
}

func main() {
	mux := http.NewServeMux()
	mux.Handle("/api/backend/", proxy("/api/backend/", httpx.Env("BACKEND_URL", "http://localhost:4000")))
	mux.Handle("/api/payment/", proxy("/api/payment/", httpx.Env("PAYMENT_URL", "http://localhost:4242")))
	root, _ := fs.Sub(static, "static")
	mux.Handle("/", http.FileServerFS(root))
	addr := ":" + httpx.Env("PORT", "3000")
	log.Printf("frontend listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
