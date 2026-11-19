// Command backend is the Pinecone store API (SPEC section 3).
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"pinecone/internal/httpx"
)

const schema = `
CREATE TABLE IF NOT EXISTS products (
  sku text PRIMARY KEY, name text NOT NULL, description text NOT NULL DEFAULT '', category text NOT NULL DEFAULT '',
  price_cents bigint NOT NULL, currency text NOT NULL, inventory bigint NOT NULL CHECK (inventory >= 0),
  status text NOT NULL, updated_at text NOT NULL);
CREATE TABLE IF NOT EXISTS plans (
  plan_id text PRIMARY KEY, name text NOT NULL, price_cents bigint NOT NULL, currency text NOT NULL,
  interval text NOT NULL, active boolean NOT NULL);
CREATE TABLE IF NOT EXISTS orders (
  order_id text PRIMARY KEY, customer_email text NOT NULL, status text NOT NULL, amount_cents bigint NOT NULL,
  currency text NOT NULL, payment_id text, client_secret text, last_event_created bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS order_items (
  order_id text NOT NULL REFERENCES orders, sku text NOT NULL, quantity bigint NOT NULL, unit_price_cents bigint NOT NULL);
CREATE TABLE IF NOT EXISTS subscriptions (
  seq bigserial, subscription_id text PRIMARY KEY, customer_email text NOT NULL, plan_id text NOT NULL,
  status text NOT NULL, amount_cents bigint NOT NULL, currency text NOT NULL, payment_id text, client_secret text,
  last_event_created bigint NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS subscriptions_email ON subscriptions (customer_email, seq);
CREATE TABLE IF NOT EXISTS webhook_events (
  event_id text PRIMARY KEY, type text NOT NULL, created bigint NOT NULL, payload jsonb NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now());
TRUNCATE products, plans, order_items, orders, subscriptions, webhook_events;`

type server struct {
	db            *pgxpool.Pool
	paymentURL    string
	apiKey        string
	webhookSecret string
	dataDir       string
	legacySecret  string

	mu     sync.Mutex
	report map[string]Section
}

func main() {
	ctx := context.Background()
	s := &server{
		db:            httpx.ConnectDB(ctx, httpx.Env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/backend?sslmode=disable"), schema+migrationSchema),
		paymentURL:    httpx.Env("PAYMENT_URL", "http://localhost:4242"),
		apiKey:        httpx.Env("PAYMENT_API_KEY", "sk_test_pinecone_local"),
		webhookSecret: httpx.Env("PAYMENT_WEBHOOK_SECRET", "whsec_test_pinecone_local"),
		dataDir:       httpx.Env("DATA_DIR", "data"),
		legacySecret:  httpx.Env("LEGACY_WEBHOOK_SECRET", "whsec_legacy_pinecone_2025"),
	}
	if err := s.importCatalog(ctx); err != nil {
		log.Fatalf("import: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /products", s.listProducts)
	mux.HandleFunc("GET /products/{sku}", s.getProduct)
	mux.HandleFunc("POST /products", s.createProduct)
	mux.HandleFunc("POST /products/{sku}/restock", s.restock)
	mux.HandleFunc("GET /plans", s.listPlans)
	mux.HandleFunc("GET /admin/import-report", s.importReport)
	mux.HandleFunc("POST /orders", s.createOrder)
	mux.HandleFunc("GET /orders/{id}", s.getOrder)
	mux.HandleFunc("POST /subscriptions", s.createSubscription)
	mux.HandleFunc("GET /customers/{email}/subscription", s.getSubscription)
	mux.HandleFunc("POST /webhooks/payments", s.webhook)
	mux.HandleFunc("POST /admin/migrations/legacy", s.migrateLegacy)

	addr := ":" + httpx.Env("PORT", "4000")
	log.Printf("backend listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, httpx.CORS(mux)))
}

// importCatalog loads products.csv and plans.csv from DATA_DIR into Postgres and keeps the report.
func (s *server) importCatalog(ctx context.Context) error {
	pf, err := os.Open(filepath.Join(s.dataDir, "products.csv"))
	if err != nil {
		return err
	}
	defer pf.Close()
	products, psec, err := LoadProducts(pf)
	if err != nil {
		return err
	}
	lf, err := os.Open(filepath.Join(s.dataDir, "plans.csv"))
	if err != nil {
		return err
	}
	defer lf.Close()
	plans, lsec, err := LoadPlans(lf)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, p := range products {
		if _, err := tx.Exec(ctx, `INSERT INTO products VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			p.SKU, p.Name, p.Description, p.Category, p.PriceCents, p.Currency, p.Inventory, p.Status, p.UpdatedAt); err != nil {
			return err
		}
	}
	for _, p := range plans {
		if _, err := tx.Exec(ctx, `INSERT INTO plans VALUES ($1,$2,$3,$4,$5,$6)`,
			p.PlanID, p.Name, p.PriceCents, p.Currency, p.Interval, p.Active); err != nil {
			return err
		}
	}
	s.report = map[string]Section{"products": psec, "plans": lsec}
	log.Printf("imported %d/%d products, %d/%d plans", psec.Imported, psec.RowsRead, lsec.Imported, lsec.RowsRead)
	return tx.Commit(ctx)
}

func (s *server) importReport(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	httpx.JSON(w, 200, s.report)
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
