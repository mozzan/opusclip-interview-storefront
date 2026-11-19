package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"pinecone/internal/httpx"
)

const productCols = `sku, name, description, category, price_cents, currency, inventory`

func scanProduct(row pgx.Row) (*Product, error) {
	var p Product
	err := row.Scan(&p.SKU, &p.Name, &p.Description, &p.Category, &p.PriceCents, &p.Currency, &p.Inventory)
	return &p, err
}

func (s *server) listProducts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT `+productCols+` FROM products WHERE status='active' ORDER BY sku`)
	if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Product, error) { return scanProduct(row) })
	if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	httpx.JSON(w, 200, list)
}

func (s *server) getProduct(w http.ResponseWriter, r *http.Request) {
	p, err := scanProduct(s.db.QueryRow(r.Context(),
		`SELECT `+productCols+` FROM products WHERE sku=$1 AND status='active'`, CanonicalSKU(r.PathValue("sku"))))
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, 404, "not_found", "no product %s", r.PathValue("sku"))
		return
	} else if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	httpx.JSON(w, 200, p)
}

func (s *server) createProduct(w http.ResponseWriter, r *http.Request) {
	b, err := httpx.DecodeBody(r)
	if err != nil {
		httpx.Error(w, 400, "invalid_request", "%v", err)
		return
	}
	bad := func(format string, a ...any) { httpx.Error(w, 400, "invalid_product", format, a...) }
	var p Product
	var ok bool
	if p.SKU, ok = b.Str("sku"); !ok {
		bad("sku must be a string")
		return
	}
	if p.Name, ok = b.Str("name"); !ok {
		bad("name must be a string")
		return
	}
	if p.PriceCents, ok = b.Int("price_cents"); !ok {
		bad("price_cents must be a JSON integer")
		return
	}
	if p.Inventory, ok = b.Int("inventory"); !ok {
		bad("inventory must be a JSON integer")
		return
	}
	p.Currency, _ = b.Str("currency")
	p.Description, _ = b.Str("description")
	p.Category, _ = b.Str("category")
	if p.Status, ok = b.Str("status"); !ok {
		p.Status = "active"
	}
	if err := p.Validate(); err != nil {
		bad("%v", err)
		return
	}
	tag, err := s.db.Exec(r.Context(), `INSERT INTO products VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (sku) DO NOTHING`,
		p.SKU, p.Name, p.Description, p.Category, p.PriceCents, p.Currency, p.Inventory, p.Status, now())
	if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, 409, "sku_exists", "SKU %s already exists", p.SKU)
		return
	}
	httpx.JSON(w, 201, p)
}

func (s *server) restock(w http.ResponseWriter, r *http.Request) {
	b, err := httpx.DecodeBody(r)
	if err != nil {
		httpx.Error(w, 400, "invalid_request", "%v", err)
		return
	}
	q, ok := b.Int("quantity")
	if !ok || q <= 0 {
		httpx.Error(w, 400, "invalid_quantity", "quantity must be a positive integer")
		return
	}
	p, err := scanProduct(s.db.QueryRow(r.Context(),
		`UPDATE products SET inventory = inventory + $2, updated_at = $3 WHERE sku=$1 RETURNING `+productCols,
		CanonicalSKU(r.PathValue("sku")), q, now()))
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, 404, "not_found", "no product %s", r.PathValue("sku"))
		return
	} else if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	httpx.JSON(w, 200, p)
}

func (s *server) listPlans(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT plan_id, name, price_cents, currency, interval FROM plans WHERE active ORDER BY price_cents`)
	if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Plan, error) {
		var p Plan
		err := row.Scan(&p.PlanID, &p.Name, &p.PriceCents, &p.Currency, &p.Interval)
		return p, err
	})
	if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	httpx.JSON(w, 200, list)
}

var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// canonicalEmail trims and lower-cases an email; ok is false when it does not look like one.
func canonicalEmail(s string) (string, bool) {
	e := strings.ToLower(strings.TrimSpace(s))
	return e, emailPattern.MatchString(e)
}

type paymentRef struct {
	ID           string `json:"id"`
	ClientSecret string `json:"client_secret,omitempty"`
}

// createPayment asks the payment service for a payment. idemKey makes the call safe to retry.
func (s *server) createPayment(ctx context.Context, amount int64, description, idemKey string, metadata map[string]string) (paymentRef, error) {
	var out paymentRef
	status, err := httpx.PostJSON(ctx, s.paymentURL+"/payments",
		map[string]string{"Authorization": "Bearer " + s.apiKey, "Idempotency-Key": idemKey},
		map[string]any{"amount_cents": amount, "currency": "USD", "description": description, "metadata": metadata}, &out)
	if err == nil && status >= 300 {
		err = fmt.Errorf("payment service returned %d", status)
	}
	return out, err
}

type orderItem struct {
	SKU            string `json:"sku"`
	Quantity       int64  `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"`
}

type order struct {
	OrderID       string      `json:"order_id"`
	Status        string      `json:"status"`
	CustomerEmail string      `json:"customer_email"`
	Items         []orderItem `json:"items"`
	AmountCents   int64       `json:"amount_cents"`
	Currency      string      `json:"currency"`
	Payment       *paymentRef `json:"payment"`
}

func (s *server) createOrder(w http.ResponseWriter, r *http.Request) {
	b, err := httpx.DecodeBody(r)
	if err != nil {
		httpx.Error(w, 400, "invalid_request", "%v", err)
		return
	}
	rawEmail, _ := b.Str("customer_email")
	email, ok := canonicalEmail(rawEmail)
	if !ok {
		httpx.Error(w, 400, "invalid_email", "customer_email is not a valid email")
		return
	}
	rawItems, _ := b["items"].([]any)
	if len(rawItems) == 0 {
		httpx.Error(w, 400, "invalid_items", "items must be a non-empty list")
		return
	}
	// Merge repeated SKUs so the inventory check sees the full quantity.
	qty := map[string]int64{}
	var skus []string
	for _, it := range rawItems {
		m, _ := it.(map[string]any)
		sku, _ := m["sku"].(string)
		q, ok := httpx.AsInt(m["quantity"])
		if sku == "" || !ok || q <= 0 {
			httpx.Error(w, 400, "invalid_items", "each item needs a sku and a positive integer quantity")
			return
		}
		sku = CanonicalSKU(sku)
		if _, seen := qty[sku]; !seen {
			skus = append(skus, sku)
		}
		qty[sku] += q
	}

	o := order{OrderID: httpx.NewID("ord_"), Status: "pending_payment", CustomerEmail: email, Currency: "USD", Items: []orderItem{}}
	for _, sku := range skus {
		p, err := scanProduct(s.db.QueryRow(r.Context(), `SELECT `+productCols+` FROM products WHERE sku=$1 AND status='active'`, sku))
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, 404, "unknown_sku", "no product %s", sku)
			return
		} else if err != nil {
			httpx.Error(w, 500, "internal", "%v", err)
			return
		}
		if qty[sku] > p.Inventory {
			httpx.Error(w, 409, "insufficient_inventory", "Only %d left of %s", p.Inventory, sku)
			return
		}
		o.Items = append(o.Items, orderItem{SKU: sku, Quantity: qty[sku], UnitPriceCents: p.PriceCents})
		o.AmountCents += qty[sku] * p.PriceCents
	}

	tx, err := s.db.Begin(r.Context())
	if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `INSERT INTO orders (order_id, customer_email, status, amount_cents, currency) VALUES ($1,$2,$3,$4,$5)`,
		o.OrderID, o.CustomerEmail, o.Status, o.AmountCents, o.Currency); err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	for _, it := range o.Items {
		if _, err := tx.Exec(r.Context(), `INSERT INTO order_items VALUES ($1,$2,$3,$4)`, o.OrderID, it.SKU, it.Quantity, it.UnitPriceCents); err != nil {
			httpx.Error(w, 500, "internal", "%v", err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}

	pay, err := s.createPayment(r.Context(), o.AmountCents, "Order "+o.OrderID, "order:"+o.OrderID, map[string]string{"order_id": o.OrderID})
	if err != nil {
		httpx.Error(w, 502, "payment_unavailable", "could not create payment: %v", err)
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE orders SET payment_id=$2, client_secret=$3 WHERE order_id=$1`, o.OrderID, pay.ID, pay.ClientSecret); err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	o.Payment = &pay
	httpx.JSON(w, 201, o)
}

func (s *server) getOrder(w http.ResponseWriter, r *http.Request) {
	o := order{Items: []orderItem{}}
	var payID *string
	err := s.db.QueryRow(r.Context(), `SELECT order_id, status, customer_email, amount_cents, currency, payment_id FROM orders WHERE order_id=$1`,
		r.PathValue("id")).Scan(&o.OrderID, &o.Status, &o.CustomerEmail, &o.AmountCents, &o.Currency, &payID)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, 404, "not_found", "no order %s", r.PathValue("id"))
		return
	} else if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	if payID != nil {
		o.Payment = &paymentRef{ID: *payID}
	}
	rows, err := s.db.Query(r.Context(), `SELECT sku, quantity, unit_price_cents FROM order_items WHERE order_id=$1`, o.OrderID)
	if err == nil {
		o.Items, err = pgx.CollectRows(rows, pgx.RowToStructByPos[orderItem])
	}
	if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	httpx.JSON(w, 200, o)
}

func (s *server) createSubscription(w http.ResponseWriter, r *http.Request) {
	b, err := httpx.DecodeBody(r)
	if err != nil {
		httpx.Error(w, 400, "invalid_request", "%v", err)
		return
	}
	rawEmail, _ := b.Str("customer_email")
	email, ok := canonicalEmail(rawEmail)
	if !ok {
		httpx.Error(w, 400, "invalid_email", "customer_email is not a valid email")
		return
	}
	planID, ok := b.Str("plan_id")
	if !ok || strings.TrimSpace(planID) == "" {
		httpx.Error(w, 400, "invalid_plan", "plan_id is required")
		return
	}
	var price int64
	var currency string
	err = s.db.QueryRow(r.Context(), `SELECT price_cents, currency FROM plans WHERE plan_id=$1 AND active`, strings.TrimSpace(planID)).Scan(&price, &currency)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, 404, "unknown_plan", "no plan %s on sale", planID)
		return
	} else if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	planID = strings.TrimSpace(planID)

	id := httpx.NewID("sub_")
	// The insert only happens when the customer has no active subscription, so two racing requests can't both win.
	tag, err := s.db.Exec(r.Context(), `
		INSERT INTO subscriptions (subscription_id, customer_email, plan_id, status, amount_cents, currency)
		SELECT $1,$2,$3,'pending',$4,$5
		WHERE NOT EXISTS (SELECT 1 FROM subscriptions WHERE customer_email=$2 AND status='active')`,
		id, email, planID, price, currency)
	if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, 409, "already_subscribed", "%s already has an active subscription", email)
		return
	}
	pay, err := s.createPayment(r.Context(), price, "Subscription "+id, "subscription:"+id, map[string]string{"subscription_id": id})
	if err != nil {
		httpx.Error(w, 502, "payment_unavailable", "could not create payment: %v", err)
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE subscriptions SET payment_id=$2, client_secret=$3 WHERE subscription_id=$1`, id, pay.ID, pay.ClientSecret); err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	httpx.JSON(w, 201, map[string]any{
		"subscription_id": id, "status": "pending", "customer_email": email, "plan_id": planID,
		"amount_cents": price, "currency": currency, "payment": pay,
	})
}

func (s *server) getSubscription(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	var status string
	var planID *string
	err := s.db.QueryRow(r.Context(), `SELECT status, plan_id FROM subscriptions WHERE customer_email=$1 ORDER BY seq DESC LIMIT 1`, email).Scan(&status, &planID)
	if errors.Is(err, pgx.ErrNoRows) {
		status = "none"
	} else if err != nil {
		httpx.Error(w, 500, "internal", "%v", err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"customer_email": email, "status": status, "plan_id": planID})
}
