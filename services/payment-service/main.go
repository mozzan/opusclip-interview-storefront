// Command payment-service is the fake payment processor (SPEC section 4).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pinecone/internal/httpx"
	"pinecone/internal/signing"
)

const schema = `
CREATE TABLE IF NOT EXISTS payments (
  id              text PRIMARY KEY,
  amount_cents    bigint NOT NULL,
  currency        text NOT NULL,
  description     text NOT NULL DEFAULT '',
  metadata        jsonb NOT NULL DEFAULT '{}',
  client_secret   text NOT NULL,
  status          text NOT NULL,
  failure_reason  text,
  card_last4      text,
  created         bigint NOT NULL,
  idempotency_key text UNIQUE
);`

type server struct {
	db            *pgxpool.Pool
	apiKey        string
	webhookSecret string
	webhookURL    string
}

type payment struct {
	ID            string          `json:"id"`
	Status        string          `json:"status"`
	AmountCents   int64           `json:"amount_cents"`
	Currency      string          `json:"currency"`
	Description   string          `json:"description"`
	Metadata      json.RawMessage `json:"metadata"`
	ClientSecret  string          `json:"client_secret,omitempty"`
	FailureReason *string         `json:"failure_reason"`
	CardLast4     *string         `json:"card_last4"`
	Created       int64           `json:"created"`
}

const paymentCols = `id, status, amount_cents, currency, description, metadata, client_secret, failure_reason, card_last4, created`

func scanPayment(row pgx.Row) (*payment, error) {
	var p payment
	err := row.Scan(&p.ID, &p.Status, &p.AmountCents, &p.Currency, &p.Description, &p.Metadata,
		&p.ClientSecret, &p.FailureReason, &p.CardLast4, &p.Created)
	return &p, err
}

func main() {
	ctx := context.Background()
	s := &server{
		db:            httpx.ConnectDB(ctx, httpx.Env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/payments?sslmode=disable"), schema),
		apiKey:        httpx.Env("PAYMENT_API_KEY", "sk_test_pinecone_local"),
		webhookSecret: httpx.Env("PAYMENT_WEBHOOK_SECRET", "whsec_test_pinecone_local"),
		webhookURL:    httpx.Env("WEBHOOK_URL", "http://localhost:4000/webhooks/payments"),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /payments", s.requireKey(s.create))
	mux.HandleFunc("GET /payments/{id}", s.requireKey(s.get))
	mux.HandleFunc("POST /payments/{id}/confirm", s.confirm)
	addr := ":" + httpx.Env("PORT", "4242")
	log.Printf("payment-service listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, httpx.CORS(mux)))
}

func (s *server) requireKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.apiKey {
			httpx.Error(w, 401, "unauthorized", "missing or invalid API key")
			return
		}
		next(w, r)
	}
}

func (s *server) create(w http.ResponseWriter, r *http.Request) {
	b, err := httpx.DecodeBody(r)
	if err != nil {
		httpx.Error(w, 400, "invalid_request", "%v", err)
		return
	}
	amount, ok := b.Int("amount_cents")
	if !ok || amount <= 0 {
		httpx.Error(w, 400, "invalid_amount", "amount_cents must be a positive integer")
		return
	}
	if cur, _ := b.Str("currency"); cur != "USD" {
		httpx.Error(w, 400, "invalid_currency", "currency must be USD")
		return
	}
	desc, _ := b.Str("description")
	meta := json.RawMessage("{}")
	if m, ok := b["metadata"].(map[string]any); ok {
		meta, _ = json.Marshal(m)
	}

	var key *string
	if k := strings.TrimSpace(r.Header.Get("Idempotency-Key")); k != "" {
		key = &k
	}
	id := httpx.NewID("pay_")
	secret := id + "_secret_" + httpx.NewID("")
	tag, err := s.db.Exec(r.Context(), `
		INSERT INTO payments (id, amount_cents, currency, description, metadata, client_secret, status, created, idempotency_key)
		VALUES ($1,$2,'USD',$3,$4,$5,'requires_confirmation',$6,$7)
		ON CONFLICT (idempotency_key) DO NOTHING`, id, amount, desc, meta, secret, time.Now().Unix(), key)
	if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	status := 201
	where, arg := "id = $1", any(id)
	if tag.RowsAffected() == 0 { // replay of an earlier request with the same key
		status, where, arg = 200, "idempotency_key = $1", *key
	}
	p, err := scanPayment(s.db.QueryRow(r.Context(), "SELECT "+paymentCols+" FROM payments WHERE "+where, arg))
	if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	httpx.JSON(w, status, p)
}

func (s *server) get(w http.ResponseWriter, r *http.Request) {
	p, err := scanPayment(s.db.QueryRow(r.Context(), "SELECT "+paymentCols+" FROM payments WHERE id = $1", r.PathValue("id")))
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, 404, "not_found", "no such payment")
		return
	} else if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	httpx.JSON(w, 200, p)
}

var sixteenDigits = regexp.MustCompile(`^\d{16}$`)

// outcome maps a test card to its result (SPEC section 4). duplicate means the webhook is sent twice.
func outcome(card string) (status string, reason *string, duplicate bool) {
	fail := func(r string) (string, *string, bool) { return "failed", &r, false }
	switch card {
	case "4242424242424242":
		return "succeeded", nil, false
	case "4000000000000077":
		return "succeeded", nil, true
	case "4000000000009995":
		return fail("insufficient_funds")
	default:
		return fail("card_declined")
	}
}

func (s *server) confirm(w http.ResponseWriter, r *http.Request) {
	b, err := httpx.DecodeBody(r)
	if err != nil {
		httpx.Error(w, 400, "invalid_request", "%v", err)
		return
	}
	p, err := scanPayment(s.db.QueryRow(r.Context(), "SELECT "+paymentCols+" FROM payments WHERE id = $1", r.PathValue("id")))
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, 404, "not_found", "no such payment")
		return
	} else if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	if secret, _ := b.Str("client_secret"); secret != p.ClientSecret {
		httpx.Error(w, 401, "unauthorized", "wrong client_secret")
		return
	}
	raw, _ := b.Str("card_number")
	card := strings.ReplaceAll(raw, " ", "")
	if !sixteenDigits.MatchString(card) {
		httpx.Error(w, 400, "invalid_card", "card_number must be 16 digits")
		return
	}
	status, reason, duplicate := outcome(card)
	last4 := card[12:]
	tag, err := s.db.Exec(r.Context(), `
		UPDATE payments SET status=$2, failure_reason=$3, card_last4=$4
		WHERE id=$1 AND status='requires_confirmation'`, p.ID, status, reason, last4)
	if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, 409, "already_confirmed", "payment was already confirmed")
		return
	}
	p.Status, p.FailureReason, p.CardLast4 = status, reason, &last4

	event := map[string]any{
		"id":      httpx.NewID("evt_"),
		"type":    "payment." + status,
		"created": time.Now().Unix(),
		"data": map[string]any{
			"payment_id": p.ID, "status": status, "amount_cents": p.AmountCents, "currency": p.Currency,
			"failure_reason": reason, "metadata": p.Metadata,
		},
	}
	body, _ := json.Marshal(event)
	go s.deliver(body)
	if duplicate {
		go func() { time.Sleep(300 * time.Millisecond); s.deliver(body) }()
	}

	httpx.JSON(w, 200, map[string]any{
		"id": p.ID, "status": status, "failure_reason": reason, "card_last4": last4,
		"amount_cents": p.AmountCents, "currency": p.Currency,
	})
}

// deliver POSTs a signed event to WEBHOOK_URL, retrying with backoff until a 2xx (5 attempts).
func (s *server) deliver(body []byte) {
	client := &http.Client{Timeout: 5 * time.Second}
	backoff := 500 * time.Millisecond
	for attempt := 1; attempt <= 5; attempt++ {
		req, _ := http.NewRequest(http.MethodPost, s.webhookURL, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Payment-Signature", signing.Sign(s.webhookSecret, time.Now().Unix(), body))
		res, err := client.Do(req)
		if err == nil {
			res.Body.Close()
			if res.StatusCode < 300 {
				return
			}
			err = errors.New(res.Status)
		}
		log.Printf("webhook attempt %d failed: %v", attempt, err)
		time.Sleep(backoff)
		backoff *= 2
	}
}
