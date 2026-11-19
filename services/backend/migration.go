package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"pinecone/internal/httpx"
	"pinecone/internal/signing"
)

const migrationSchema = `
CREATE TABLE IF NOT EXISTS customers (
  email text PRIMARY KEY, customer_id text NOT NULL, full_name text NOT NULL DEFAULT '',
  legacy_status text NOT NULL DEFAULT '', legacy_plan_id text NOT NULL DEFAULT '', created_at text NOT NULL DEFAULT '');
TRUNCATE customers;`

type customerReport struct {
	RowsRead int         `json:"rows_read"`
	Imported int         `json:"imported"`
	Rejected []Rejection `json:"rejected"`
	Warnings []string    `json:"warnings"`
}

type skipped struct {
	DeliveryID string `json:"delivery_id"`
	Reason     string `json:"reason"`
}

type eventsReport struct {
	DeliveriesRead int       `json:"deliveries_read"`
	Applied        int       `json:"applied"`
	Skipped        []skipped `json:"skipped"`
}

type legacyCustomer struct {
	email, id, name, status, plan, created string
}

// loadCustomers parses customers.csv. Emails are canonicalized; a repeated email is rejected as a duplicate.
func loadCustomers(path string) ([]legacyCustomer, customerReport, error) {
	rep := customerReport{Rejected: []Rejection{}, Warnings: []string{}}
	f, err := os.Open(path)
	if err != nil {
		return nil, rep, err
	}
	defer f.Close()
	var out []legacyCustomer
	firstLine := map[string]int{}
	n, err := csvRows(f, func(line int, col func(string) string, fieldErr error) {
		reject := func(reason string) { rep.Rejected = append(rep.Rejected, Rejection{Line: line, Reason: reason}) }
		if fieldErr != nil {
			reject(fieldErr.Error())
			return
		}
		email, ok := canonicalEmail(col("email"))
		if !ok {
			reject(fmt.Sprintf("email %q is not valid", col("email")))
			return
		}
		if prev, dup := firstLine[email]; dup {
			reject(fmt.Sprintf("duplicate of line %d (emails are not case-sensitive); subscription status comes from payment events", prev))
			return
		}
		firstLine[email] = line
		c := legacyCustomer{email: email, id: strings.TrimSpace(col("customer_id")), name: strings.TrimSpace(col("full_name")),
			status: strings.TrimSpace(col("legacy_subscription_status")), plan: strings.TrimSpace(col("legacy_plan_id")),
			created: strings.TrimSpace(col("created_at"))}
		if _, err := time.Parse(time.RFC3339, c.created); err != nil {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("line %d %s: created_at %q is not ISO 8601", line, email, c.created))
		}
		out = append(out, c)
	})
	rep.RowsRead, rep.Imported = n, len(out)
	return out, rep, err
}

type delivery struct {
	DeliveryID string            `json:"delivery_id"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	ev         event
	order      int
}

func (s *server) migrateLegacy(w http.ResponseWriter, r *http.Request) {
	report, err := s.runMigration(r.Context())
	if err != nil {
		httpx.Error(w, 500, "migration_failed", "%v", err)
		return
	}
	httpx.JSON(w, 200, report)
}

func (s *server) runMigration(ctx context.Context) (map[string]any, error) {
	customers, crep, err := loadCustomers(filepath.Join(s.dataDir, "customers.csv"))
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(s.dataDir, "legacy_payment_events.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	erep := eventsReport{Skipped: []skipped{}}
	skip := func(id, format string, a ...any) {
		erep.Skipped = append(erep.Skipped, skipped{DeliveryID: id, Reason: fmt.Sprintf(format, a...)})
	}
	var valid []*delivery
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for lineNo := 1; sc.Scan(); lineNo++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		erep.DeliveriesRead++
		d := &delivery{order: lineNo}
		if err := json.Unmarshal(sc.Bytes(), d); err != nil {
			skip(fmt.Sprintf("line %d", lineNo), "unreadable delivery record: %v", err)
			continue
		}
		if _, err := signing.Verify(s.legacySecret, d.Headers["x-legacy-signature"], []byte(d.Body), time.Time{}, 0); err != nil {
			skip(d.DeliveryID, "rejected signature: %v", err)
			continue
		}
		if err := json.Unmarshal([]byte(d.Body), &d.ev); err != nil || d.ev.ID == "" {
			skip(d.DeliveryID, "signed body is not a payment event")
			continue
		}
		valid = append(valid, d)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// Apply in the order events happened, not the order they were delivered.
	sort.SliceStable(valid, func(i, j int) bool { return valid[i].ev.Created < valid[j].ev.Created })

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(4242)`); err != nil { // one migration at a time
		return nil, err
	}
	for _, c := range customers {
		if _, err := tx.Exec(ctx, `
			INSERT INTO customers VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (email) DO UPDATE SET customer_id=$2, full_name=$3, legacy_status=$4, legacy_plan_id=$5, created_at=$6`,
			c.email, c.id, c.name, c.status, c.plan, c.created); err != nil {
			return nil, err
		}
	}
	for _, d := range valid {
		reason, err := applyLegacyEvent(ctx, tx, d.ev)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d.DeliveryID, err)
		}
		if reason != "" {
			skip(d.DeliveryID, "%s", reason)
		} else {
			erep.Applied++
		}
	}
	// Point out where the old store's status disagrees with what the payment history says.
	for _, c := range customers {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM subscriptions WHERE customer_email=$1 ORDER BY seq DESC LIMIT 1`, c.email).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			status = "none"
		} else if err != nil {
			return nil, err
		}
		if legacy := c.status; legacy != status {
			crep.Warnings = append(crep.Warnings, fmt.Sprintf("%s: old store said %q (%s), payment history gives %q", c.email, legacy, c.plan, status))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	sort.SliceStable(erep.Skipped, func(i, j int) bool { return erep.Skipped[i].DeliveryID < erep.Skipped[j].DeliveryID })
	return map[string]any{"customers": crep, "events": erep}, nil
}

// applyLegacyEvent replays one verified legacy event. It returns a non-empty reason when the event is skipped.
// Each customer gets one migrated subscription, "sub_legacy_<email>"; re-runs are no-ops through webhook_events.
func applyLegacyEvent(ctx context.Context, tx pgx.Tx, ev event) (string, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO webhook_events (event_id, type, created, payload) VALUES ($1,$2,$3,'{}') ON CONFLICT DO NOTHING`,
		ev.ID, ev.Type, ev.Created)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Sprintf("event %s already processed (duplicate delivery or earlier migration run)", ev.ID), nil
	}
	email, _ := canonicalEmail(ev.Data.CustomerEmail)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM customers WHERE email=$1)`, email).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return fmt.Sprintf("customer %q is not in customers.csv", ev.Data.CustomerEmail), nil
	}
	var price int64
	var currency string
	err = tx.QueryRow(ctx, `SELECT price_cents, currency FROM plans WHERE plan_id=$1`, ev.Data.PlanID).Scan(&price, &currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Sprintf("plan %q is not in the imported plans", ev.Data.PlanID), nil
	} else if err != nil {
		return "", err
	}
	if ev.Type == "payment.succeeded" && (ev.Data.AmountCents != price || ev.Data.Currency != currency) {
		return fmt.Sprintf("paid %d %s but %s costs %d %s", ev.Data.AmountCents, ev.Data.Currency, ev.Data.PlanID, price, currency), nil
	}

	subID := "sub_legacy_" + email
	var status string
	var paymentID *string
	var last int64
	err = tx.QueryRow(ctx, `SELECT status, payment_id, last_event_created FROM subscriptions WHERE subscription_id=$1 FOR UPDATE`, subID).
		Scan(&status, &paymentID, &last)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if ev.Type == "payment.refunded" {
			return "refund for a payment that never started a subscription", nil
		}
		next := nextSubscriptionStatus("pending", ev, price, currency)
		_, err = tx.Exec(ctx, `INSERT INTO subscriptions (subscription_id, customer_email, plan_id, status, amount_cents, currency, payment_id, last_event_created)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, subID, email, ev.Data.PlanID, next, price, currency, ev.Data.PaymentID, ev.Created)
		return "", err
	case err != nil:
		return "", err
	}
	if ev.Created < last {
		return "older than an event already applied", nil
	}
	if ev.Type == "payment.refunded" && (paymentID == nil || *paymentID != ev.Data.PaymentID) {
		return fmt.Sprintf("refund for %s, which is not the subscription's current payment", ev.Data.PaymentID), nil
	}
	next := nextSubscriptionStatus(status, ev, price, currency)
	planID, payID := ev.Data.PlanID, ev.Data.PaymentID
	if ev.Type != "payment.succeeded" { // only a successful payment changes the plan or current payment
		planID, payID = "", ""
	}
	_, err = tx.Exec(ctx, `UPDATE subscriptions SET status=$2, last_event_created=$3,
		plan_id=COALESCE(NULLIF($4,''), plan_id), payment_id=COALESCE(NULLIF($5,''), payment_id),
		amount_cents=CASE WHEN $4='' THEN amount_cents ELSE $6 END WHERE subscription_id=$1`,
		subID, next, ev.Created, planID, payID, price)
	return "", err
}
