package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"pinecone/internal/httpx"
	"pinecone/internal/signing"
)

// event is a payment webhook (SPEC section 5). Legacy events carry CustomerEmail and PlanID instead of Metadata.
type event struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Created int64  `json:"created"`
	Data    struct {
		PaymentID     string         `json:"payment_id"`
		Status        string         `json:"status"`
		AmountCents   int64          `json:"amount_cents"`
		Currency      string         `json:"currency"`
		Metadata      map[string]any `json:"metadata"`
		CustomerEmail string         `json:"customer_email"`
		PlanID        string         `json:"plan_id"`
	} `json:"data"`
}

func (s *server) webhook(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		httpx.Error(w, 400, "invalid_body", "could not read body")
		return
	}
	header := r.Header.Get("X-Payment-Signature")
	if header == "" {
		httpx.Error(w, 401, "missing_signature", "X-Payment-Signature header is required")
		return
	}
	if _, err := signing.Verify(s.webhookSecret, header, raw, time.Now(), 300*time.Second); err != nil {
		httpx.Error(w, 401, "invalid_signature", "%v", err)
		return
	}
	var ev event
	if err := json.Unmarshal(raw, &ev); err != nil || ev.ID == "" {
		httpx.Error(w, 400, "invalid_event", "body is not a payment event")
		return
	}
	applied, err := s.record(r.Context(), ev, raw)
	if err != nil {
		log.Printf("webhook %s: %v", ev.ID, err)
		httpx.Error(w, 500, "internal", "could not apply event") // non-2xx so the provider retries
		return
	}
	httpx.JSON(w, 200, map[string]any{"received": true, "duplicate": !applied})
}

// record stores the event once and applies it in the same transaction. It returns false for a duplicate event id.
func (s *server) record(ctx context.Context, ev event, raw []byte) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	// A concurrent duplicate blocks here until the first commits, then sees the conflict.
	tag, err := tx.Exec(ctx, `INSERT INTO webhook_events (event_id, type, created, payload) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
		ev.ID, ev.Type, ev.Created, raw)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if id, _ := ev.Data.Metadata["order_id"].(string); id != "" {
		err = applyOrderEvent(ctx, tx, id, ev)
	} else if id, _ := ev.Data.Metadata["subscription_id"].(string); id != "" {
		err = applySubscriptionEvent(ctx, tx, id, ev)
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// applyOrderEvent moves an order along. Events older than the last one applied are ignored, which gives the
// same result as applying them in created order.
func applyOrderEvent(ctx context.Context, tx pgx.Tx, orderID string, ev event) error {
	var status, currency string
	var paymentID *string
	var amount, last int64
	err := tx.QueryRow(ctx, `SELECT status, payment_id, amount_cents, currency, last_event_created FROM orders WHERE order_id=$1 FOR UPDATE`,
		orderID).Scan(&status, &paymentID, &amount, &currency, &last)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // unknown order: acknowledge and ignore
	} else if err != nil {
		return err
	}
	if paymentID == nil || *paymentID != ev.Data.PaymentID || ev.Created < last {
		return nil
	}
	next := status
	switch ev.Type {
	case "payment.succeeded":
		if status != "paid" && ev.Data.AmountCents == amount && ev.Data.Currency == currency {
			if err := takeStock(ctx, tx, orderID); err != nil {
				return err
			}
			next = "paid"
		}
	case "payment.failed":
		if status == "pending_payment" {
			next = "payment_failed"
		}
	}
	_, err = tx.Exec(ctx, `UPDATE orders SET status=$2, last_event_created=$3 WHERE order_id=$1`, orderID, next, ev.Created)
	return err
}

// takeStock decrements inventory for a paid order. If stock ran out since the order was placed, it logs the
// oversell instead of going negative (see SOLUTION.md known gaps).
func takeStock(ctx context.Context, tx pgx.Tx, orderID string) error {
	rows, err := tx.Query(ctx, `SELECT sku, quantity FROM order_items WHERE order_id=$1`, orderID)
	if err != nil {
		return err
	}
	type line struct {
		sku string
		qty int64
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (line, error) {
		var l line
		return l, r.Scan(&l.sku, &l.qty)
	})
	if err != nil {
		return err
	}
	for _, it := range items {
		tag, err := tx.Exec(ctx, `UPDATE products SET inventory = inventory - $2, updated_at=$3 WHERE sku=$1 AND inventory >= $2`, it.sku, it.qty, now())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			log.Printf("order %s: oversold %s (wanted %d); needs manual follow-up", orderID, it.sku, it.qty)
		}
	}
	return nil
}

// applySubscriptionEvent follows the SPEC section 3 subscription rules.
func applySubscriptionEvent(ctx context.Context, tx pgx.Tx, subID string, ev event) error {
	var status string
	var paymentID *string
	var price int64
	var currency string
	var last int64
	err := tx.QueryRow(ctx, `
		SELECT s.status, s.payment_id, p.price_cents, p.currency, s.last_event_created
		FROM subscriptions s JOIN plans p USING (plan_id) WHERE s.subscription_id=$1 FOR UPDATE OF s`,
		subID).Scan(&status, &paymentID, &price, &currency, &last)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if (paymentID != nil && *paymentID != ev.Data.PaymentID) || ev.Created < last {
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE subscriptions SET status=$2, last_event_created=$3 WHERE subscription_id=$1`,
		subID, nextSubscriptionStatus(status, ev, price, currency), ev.Created)
	return err
}

func nextSubscriptionStatus(status string, ev event, price int64, currency string) string {
	switch ev.Type {
	case "payment.succeeded":
		if ev.Data.AmountCents == price && ev.Data.Currency == currency {
			return "active"
		}
	case "payment.failed":
		if status != "active" {
			return "payment_failed"
		}
	case "payment.refunded":
		return "canceled"
	}
	return status
}
