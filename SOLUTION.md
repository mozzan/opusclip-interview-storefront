# Solution notes

## How to run

```bash
docker compose up --build          # postgres + backend :4000 + payment-service :4242 + frontend :3000
npm test                           # acceptance tests (L1 + L2 pass, L3 skipped)
```

If port 3000 is taken: `FRONTEND_PORT=3001 docker compose up --build`, then `FRONTEND_URL=http://localhost:3001 npm test`.
Go unit tests: `go test ./...`.

## Stack and design

- One Go module (Go 1.26). There are three binaries in `services/`, plus shared `internal/httpx` (JSON, errors,
  CORS, DB connect) and `internal/signing` (webhook HMAC).
  The only dependency is `jackc/pgx/v5`, and routing uses the standard library.
- Postgres 16 runs in one container with two databases: `backend` and `payments`. Each service owns its own database
  and creates its schema on startup. The backend truncates its tables at startup and reloads the catalog from `DATA_DIR`
  (SPEC: nothing has to survive a restart).
- **Payment service:** payments live in Postgres. `idempotency_key` is a `UNIQUE` column, so a retry with the same
  key returns the original payment (`200`). Confirm is a conditional `UPDATE … WHERE status='requires_confirmation'`,
  so a second confirm gets `409` even if two confirms race. Webhooks go out after commit and retry 5 times with
  exponential backoff. Card `…0077` sends the same event twice.
- **Backend webhooks:**
  - The signature is checked over the raw bytes, with a constant-time compare and a 300 s tolerance.
  - Each event is inserted into `webhook_events(event_id PRIMARY KEY) ON CONFLICT DO NOTHING`, and applied in the same
    transaction. A duplicate delivery is a no-op `200`.
  - The order or subscription row is locked `FOR UPDATE`. An event whose `created` is older than the last event applied
    to that row is ignored, which gives the same result as applying events in `created` order.
  - Inventory goes down only when an order is paid, with `UPDATE … WHERE inventory >= qty`.
  - The event's `payment_id` must match the payment the order or subscription created. Its amount and currency must
    match the order total or the plan price, or the event is ignored.
- **Frontend:** a Go server with one HTML/JS page (shop, cart and checkout, subscribe, admin). It proxies
  `/api/backend/*` and `/api/payment/*`, so the card number goes browser → payment service and never touches the
  backend (SPEC section 6 allows the proxy).

## Decisions and assumptions

These are import calls to confirm with the store owner. All of them show up in `GET /admin/import-report`.

- **Prices like `24.99`, `39.50` and `89.00`** (the Ridgeway supplier feed rows) and plan `team-monthly` at `49.00` are
  **rejected**, not converted. They look like dollars, but guessing ×100 on money is riskier than leaving the item off
  the shelf.
- **Other rejections:**
  - `KNIFE-FOLD-EU` (EUR)
  - `CHAIR-CAMP-FOLD` (inventory −5)
  - `HAMMOCK-NYLON` (inventory `TBD`)
  - `BAG-STUFF-SET` (price 0)
  - `BOTTLE-KIDS-350` (no name)
- **Normalized:** `ACTIVE` and `active ` → `active`; `usd` → `USD`; SKUs are trimmed and upper-cased.
- **Duplicate SKU** ` mug-ceramic-01`: the row with the newer `updated_at` wins (line 6, $18.00, 40 in stock). The older
  row is reported as rejected.
- **Imported with warnings:**
  - the supplier's `MM/DD/YYYY HH:MM` dates (read as UTC)
  - `CafÃ© Latte Mug` (looks mis-encoded, shown as is)
  - `STICKER-PACK-10` at $2,499 (likely a typo)
- **`pro-annual` at 1900** breaks finance's 10× rule (it should be 19000). It is imported as written with a warning,
  because repricing a live plan is the owner's call.
- **Archived products** are imported but not listed or sellable.
  `starter-2024` (`active=false`) is imported but not on sale.
- **GIFT-CARD-100's description** contains text addressed to "AI coding assistants" asking to skip validation and set
  inventory to 9999. It is treated as plain product text: the row goes through the normal rules and keeps its exported
  inventory of 50.
- Emails are trimmed and lower-cased everywhere. `GET /customers/{email}/subscription` reports the most recently
  created subscription.
- `POST /orders` merges repeated SKUs before the inventory check. The backend uses `Idempotency-Key: order:<id>` /
  `subscription:<id>` when it creates payments.

## How I checked the work

- `npm test`: 51 pass, 0 fail, 3 skipped (L3).
- `go test ./...`: signature cases (wrong secret, tampered body, stale, malformed). The import runs against the real
  `data/` files: it checks row counts, each rejection above, the duplicate-SKU winner, normalization, the multiline
  description, and that GIFT-CARD-100 keeps inventory 50.
- I read the import report by hand against the CSVs.

## Known gaps and next steps

- **L3 legacy migration is not built.** The design is ready for it:
  1. Verify each delivery with `LEGACY_WEBHOOK_SECRET` and no time tolerance.
  2. Sort by `created`.
  3. Map `customer_email` + `plan_id` to a subscription.
  4. Replay through the same `webhook_events` dedup and the same `nextSubscriptionStatus`, which makes a re-run safe.
- **Overselling:** if stock runs out between order creation and payment, the paid order is kept and the oversell is
  logged rather than going negative. A real store would reserve stock or refund automatically.
- **Payment-service outage:** if creating the payment fails, the order is left `pending_payment` with no payment and
  the client gets `502`. There's no cleanup job.
- **Webhook delivery:** webhooks are delivered from in-process goroutines. A restart loses deliveries in flight; an
  outbox table would fix that.
- **Other gaps:** no admin auth (per the SPEC), and the frontend is deliberately plain.

## Working with the agent

- I used Claude Code. We brainstormed first (requirements → approaches → a short design doc in `docs/superpowers/specs/`),
  then implemented against SPEC.md and the acceptance tests.
- I chose Postgres for both services (option B) over in-memory storage for the payment service.
- I checked the agent's output against the test files themselves, not just the SPEC prose. One example: the `0077`
  card sends a duplicate delivery, and the tests wait 2 s for it.
