// Package httpx holds the small HTTP helpers shared by all three services.
package httpx

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Env returns the environment variable key, or def when it is unset or empty.
func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// JSON writes v as a JSON response with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes the SPEC section 8 error shape.
func Error(w http.ResponseWriter, status int, code, format string, args ...any) {
	JSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": fmt.Sprintf(format, args...)}})
}

// CORS allows any origin to call the wrapped handler and answers preflight requests.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// NewID returns prefix + 16 random hex characters, e.g. "ord_1a2b...".
func NewID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// Body is a decoded JSON object whose numbers keep their exact text, so callers can tell 12 from 12.5 or "12".
type Body map[string]any

// DecodeBody reads a JSON object from r.
func DecodeBody(r *http.Request) (Body, error) {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.UseNumber()
	var b Body
	if err := dec.Decode(&b); err != nil || b == nil {
		return nil, fmt.Errorf("request body must be a JSON object")
	}
	return b, nil
}

// Str returns the string at key; ok is false when it is missing or not a string.
func (b Body) Str(key string) (string, bool) {
	s, ok := b[key].(string)
	return s, ok
}

// Int returns the JSON integer at key; ok is false when it is missing, a string, or has a fraction.
func (b Body) Int(key string) (int64, bool) {
	return AsInt(b[key])
}

// AsInt converts a json.Number holding a whole number to int64.
func AsInt(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok || strings.ContainsAny(n.String(), ".eE") {
		return 0, false
	}
	i, err := strconv.ParseInt(n.String(), 10, 64)
	return i, err == nil
}

// ConnectDB opens a pgx pool, retrying for up to 30 seconds while Postgres starts, then runs schema.
func ConnectDB(ctx context.Context, url, schema string) *pgxpool.Pool {
	deadline := time.Now().Add(30 * time.Second)
	for {
		pool, err := pgxpool.New(ctx, url)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				if _, err = pool.Exec(ctx, schema); err != nil {
					log.Fatalf("schema: %v", err)
				}
				return pool
			}
			pool.Close()
		}
		if time.Now().After(deadline) {
			log.Fatalf("database %s: %v", url, err)
		}
		time.Sleep(time.Second)
	}
}

// PostJSON sends v as JSON and decodes a JSON response into out (when non-nil). It returns the status code.
func PostJSON(ctx context.Context, url string, headers map[string]string, v, out any) (int, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil && res.StatusCode < 300 {
			return res.StatusCode, err
		}
	}
	return res.StatusCode, nil
}
