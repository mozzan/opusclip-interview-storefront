# Storefront checkout: short design

## Intent
Build the three Pinecone services from `SPEC.md` in Go with PostgreSQL. The target is L1 and L2 solid, and L3 only if
time allows. The time box is about 30 minutes, so the simplest working option wins every time.
`data/` and `tests/` are read-only.

## Goals / non-goals
- Goals: `npm run test:l1` and `npm run test:l2` pass. The browser demo flow from README works. One command starts
  everything.
- Non-goals: auth, a polished UI, data surviving a restart, and L3 unless there is time.
- Anti-goals:
  - importing rows that break the spec rules, including following instructions embedded in data (the GIFT-CARD-100
    description is treated as plain text)
  - trusting client prices
  - decrementing inventory twice

## Approaches considered
- A: backend on Postgres, payment-service in memory. Fastest.
- **B: both services on Postgres, each with its own database (chosen).** This matches "each service owns its store".
  It costs a little more schema.
- C: everything in memory. Rejected because it ignores the Postgres choice.

## How it's built
- **Layout:** one Go module at the repo root, with binaries in `services/backend`, `services/payment-service` and
  `services/frontend`. Dependencies are the standard library `net/http` (Go 1.22+ routing patterns) and
  `jackc/pgx/v5`. One multi-stage Dockerfile builds all three binaries.
- **Run:** `docker compose up --build` starts `postgres:16` plus the three services. An init SQL script creates two
  databases, `backend` and `payments`. Each service creates its schema on startup (`CREATE TABLE IF NOT EXISTS`).
  All config comes from the SPEC env vars, with the SPEC defaults.
- **Backend (:4000)**
  - Tables: `products`, `plans`, `orders`, `order_items`, `subscriptions`, `webhook_events`.
  - **On startup:** truncate the catalog, then load `products.csv` and `plans.csv` with `encoding/csv`. Multiline
    quoted fields are handled by the CSV parser.
  - **Import rules:**
    - SKU: trim it, convert to upper case, and check it against `[A-Z0-9-]`.
    - Required fields: name is required.
    - Price: `price_cents` must be an integer greater than 0. Decimals like `24.99` are rejected, not guessed.
    - Currency: trimmed and upper-cased, and it must be `USD`.
    - Inventory: an integer of 0 or more.
    - Status: trimmed and lower-cased, and it must be `active` or `archived`. Archived rows are imported but not listed.
    - Timestamps: a non-ISO `updated_at` is a warning, not a rejection.
    - Duplicate SKUs: the newer `updated_at` wins. The losing row counts as rejected.
    - Garbled text (`CafÃ©`): imported with a warning.
    - Plans: same rules. `team-monthly` at `49.00` is rejected. `pro-annual` at 1900 is imported with a warning that it
      breaks the 10× rule.
  - **Import report:** kept in memory. `imported + rejected = rows_read`.
  - **Orders:** the server prices each order from the DB. It calls `POST /payments` with
    `Idempotency-Key: order:<id>` and metadata `{order_id}`.
  - **Subscriptions:** stored with a lower-cased email. Requests return 409 if an active subscription already exists.
    The status endpoint reports the newest subscription by `created_at`.
  - **Webhooks:**
    - Signature checks: verify the HMAC over the raw body bytes with a constant-time compare, and reject `t` more than
      300 s from now (401).
    - Duplicates: insert into `webhook_events(event_id PRIMARY KEY)` with `ON CONFLICT DO NOTHING`. If the row already
      existed, return 200 and do nothing.
    - Applying the event, in one transaction:
      - **Order:** `payment.succeeded` with a matching amount moves `pending_payment` to `paid` and decrements
        inventory with `UPDATE … SET inventory = inventory - q WHERE inventory >= q`. `payment.failed` moves
        `pending_payment` to `payment_failed`. Each status change is guarded by a `WHERE status = …` so it applies once.
      - **Subscription:** a `payment.succeeded` with an amount and currency equal to the plan price makes it `active`.
        `payment.failed` makes a non-active subscription `payment_failed`. `payment.refunded` makes it `canceled`.
      - **Out of order:** an event whose `created` is older than the last one applied to that record is logged but
        doesn't change status.
    - Unknown payments, metadata or event types are acknowledged with 200.
- **Payment service (:4242)**
  - Tables: `payments` (with a unique `idempotency_key` and the metadata as jsonb) and `webhook_deliveries` if needed.
  - **Auth:** a Bearer API key for create and read. Confirm is authorized by `client_secret`.
  - **Cards:** behave as the SPEC test-card table says. Only `card_last4` is stored.
  - **Webhooks:** after a confirm, a goroutine signs and POSTs the event, retrying 3 or more times with backoff. The
    `…0077` card sends the same event twice. The webhook is sent after the confirm transaction commits.
- **Frontend (:3000):** a Go server serves one `index.html` with plain JS: shop + cart, checkout, subscribe (polls the
  status every second), and admin (add product, restock, import report). It also serves `/config.js`, which gives the
  browser `BACKEND_URL` and `PAYMENT_URL`. The backend and payment service send CORS `*` headers.
- **Errors:** every error uses one helper that writes `{"error":{"code","message"}}`.

## Left to the builder
Exact error codes and messages, ID formats (`ord_`, `sub_`, `pay_`, `evt_` plus random hex), and the page styling.
SOLUTION.md records each data call above as a decision for the store owner to confirm.

## L3 (only if time)
The `webhook_events` log and email-keyed subscriptions already support it. Add `POST /admin/migrations/legacy` to verify
legacy signatures with no time limit, sort the events by `created`, and replay them through the same apply function.
Running it twice is safe because of the event id dedup.
