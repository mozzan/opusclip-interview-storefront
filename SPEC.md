# Specification

This is the contract the acceptance tests check. Anything it does not pin down is your call. Write those calls down in
`SOLUTION.md`.

- [1. Services](#1-services)
- [2. Data and business rules](#2-data-and-business-rules)
- [3. Backend API](#3-backend-api)
- [4. Payment service API](#4-payment-service-api)
- [5. Webhooks](#5-webhooks)
- [6. Frontend](#6-frontend)
- [7. Level 3: migrate the old store's subscribers](#7-level-3-migrate-the-old-stores-subscribers)
- [8. Errors](#8-errors)

## 1. Services

| Service           | Default port | What it does                                                                             |
| ----------------- | ------------ | ---------------------------------------------------------------------------------------- |
| `frontend`        | 3000         | The storefront: shop, cart, checkout, subscribe, and admin pages.                        |
| `backend`         | 4000         | Store API: products, inventory, plans, orders, subscriptions, and the webhook receiver.  |
| `payment-service` | 4242         | A fake payment processor. It confirms test cards and sends signed webhooks to the backend. |

Services talk to each other over HTTP only. Each service has its own data store. In-memory is fine, and nothing needs to
survive a restart.

### Configuration

Use these environment variables. The defaults below are the values the tests use.

| Variable                 | Used by                    | Default                                     |
| ------------------------ | -------------------------- | ------------------------------------------- |
| `PAYMENT_API_KEY`        | backend, payment-service   | `sk_test_pinecone_local`                    |
| `PAYMENT_WEBHOOK_SECRET` | backend, payment-service   | `whsec_test_pinecone_local`                 |
| `WEBHOOK_URL`            | payment-service            | `http://localhost:4000/webhooks/payments`   |
| `PAYMENT_URL`            | backend, frontend          | `http://localhost:4242`                     |
| `BACKEND_URL`            | frontend                   | `http://localhost:4000`                     |
| `DATA_DIR`               | backend                    | the repository's `data/` folder             |
| `LEGACY_WEBHOOK_SECRET`  | backend (Level 3)          | `whsec_legacy_pinecone_2025`                |

## 2. Data and business rules

When the backend starts, it loads `products.csv` and `plans.csv` from `DATA_DIR`. See [`data/README.md`](data/README.md)
for where each file comes from.

### Products

| Field         | Rule                                                                                                   |
| ------------- | ------------------------------------------------------------------------------------------------------ |
| `sku`         | Unique. Case does not matter, and neither does surrounding whitespace. The canonical form is upper case: `A-Z`, `0-9` and `-`. |
| `name`        | Required. Customers see it.                                                                            |
| `description` | Optional free text.                                                                                    |
| `category`    | Optional free text.                                                                                    |
| `price_cents` | A whole number of US cents, greater than 0.                                                            |
| `currency`    | The store sells in US dollars only (`USD`).                                                            |
| `inventory`   | Units available to sell, as a whole number of 0 or more.                                               |
| `status`      | `active` or `archived`. Only active products are sold.                                                 |
| `updated_at`  | ISO 8601 UTC timestamp of the last change.                                                             |

### Plans

| Field         | Rule                                                                 |
| ------------- | -------------------------------------------------------------------- |
| `plan_id`     | Unique.                                                              |
| `price_cents` | A whole number of US cents, greater than 0, charged once per interval. |
| `currency`    | `USD`.                                                               |
| `interval`    | `month` or `year`.                                                   |
| `active`      | `true` or `false`. Only active plans can be bought.                  |

Pricing rule from finance: an annual plan costs 10 times the monthly price of the same tier. That gives the customer two
months free.

### Customers

A customer is identified by email address. Email addresses are not case-sensitive.

### Import report

The backend must account for every row it reads. `GET /admin/import-report` returns:

```json
{
  "products": {
    "rows_read": 0,
    "imported": 0,
    "rejected": [{ "line": 12, "sku": "ABC-1", "reason": "human-readable reason" }],
    "warnings": []
  },
  "plans": { "rows_read": 0, "imported": 0, "rejected": [], "warnings": [] }
}
```

- `rows_read` counts data records, not the header.
- `imported + rejected.length` must equal `rows_read`.
- `warnings` is optional free-form output for anything you imported that a person should look at.

## 3. Backend API

All bodies are JSON. Admin endpoints need no authentication in this exercise.

### Health

`GET /health` returns `200 {"status":"ok"}`.

### Products and inventory

`GET /products` returns `200` with the products that can be sold:

```json
[
  {
    "sku": "TEE-CLASSIC-BLK",
    "name": "Classic Tee - Black",
    "description": "Heavyweight 220 gsm cotton tee.",
    "category": "apparel",
    "price_cents": 2400,
    "currency": "USD",
    "inventory": 120
  }
]
```

`GET /products/{sku}` returns one product, or `404` if no product can be sold under that SKU. The lookup is not
case-sensitive.

`POST /products` adds a product (admin):

```json
{ "sku": "MUG-NEW-01", "name": "New Mug", "price_cents": 1800, "currency": "USD", "inventory": 25 }
```

- Returns `201` with the product in canonical form.
- Returns `400` when a field breaks a rule in section 2. Numbers must be JSON integers, not strings.
- Returns `409` when the SKU already exists.

`POST /products/{sku}/restock` adds stock (admin): `{ "quantity": 10 }`, where `quantity` is a positive integer.

- Returns `200` with the updated product.
- Returns `400` for a bad quantity and `404` for an unknown SKU.

### Plans

`GET /plans` returns `200` with the plans that can be bought:

```json
[{ "plan_id": "basic-monthly", "name": "Basic Monthly", "price_cents": 900, "currency": "USD", "interval": "month" }]
```

### Orders (one-off purchases)

`POST /orders` request:

```json
{ "customer_email": "sam@example.com", "items": [{ "sku": "TEE-CLASSIC-BLK", "quantity": 2 }] }
```

`201` response:

```json
{
  "order_id": "ord_123",
  "status": "pending_payment",
  "customer_email": "sam@example.com",
  "items": [{ "sku": "TEE-CLASSIC-BLK", "quantity": 2, "unit_price_cents": 2400 }],
  "amount_cents": 4800,
  "currency": "USD",
  "payment": { "id": "pay_abc", "client_secret": "pay_abc_secret_xyz" }
}
```

- The backend sets the price. It ignores any price the client sends.
- The backend creates the payment with the payment service, using metadata `{ "order_id": "<order_id>" }`.
- Returns `400` for a bad email, an empty `items` list, or a quantity that is not a positive integer.
- Returns `404` for an unknown SKU and `409` when a quantity is more than the inventory.

`GET /orders/{order_id}` returns the same shape. `payment.client_secret` may be left out. `status` is one of
`pending_payment`, `paid` or `payment_failed`.

Inventory goes down when an order is paid, not when it is created.

### Subscriptions

`POST /subscriptions` request: `{ "customer_email": "sam@example.com", "plan_id": "basic-monthly" }`.

`201` response:

```json
{
  "subscription_id": "sub_123",
  "status": "pending",
  "customer_email": "sam@example.com",
  "plan_id": "basic-monthly",
  "amount_cents": 900,
  "currency": "USD",
  "payment": { "id": "pay_def", "client_secret": "pay_def_secret_xyz" }
}
```

- The payment metadata is `{ "subscription_id": "<subscription_id>" }`.
- Returns `400` for a bad request and `404` for an unknown or inactive plan.
- Returns `409` when the customer already has an active subscription.

`GET /customers/{email}/subscription` always returns `200`:

```json
{ "customer_email": "sam@example.com", "status": "active", "plan_id": "basic-monthly" }
```

- `status` is one of `none`, `pending`, `active`, `payment_failed` or `canceled`.
- For a customer with no subscription, it returns `{"status":"none","plan_id":null}`.
- When a customer has more than one subscription, it reports the most recent.

Subscription rules:

- A subscription becomes `active` only when a `payment.succeeded` event arrives with an amount and currency equal to the
  plan's price.
- A `payment.failed` event moves a subscription that is not active to `payment_failed`.
- A `payment.refunded` event moves a subscription to `canceled`.

### Import report

`GET /admin/import-report` is described in section 2.

### Webhook receiver

`POST /webhooks/payments` is described in section 5.

## 4. Payment service API

This is a stand-in for a real processor. Card numbers never reach the backend: the browser sends them straight to the
payment service.

### Authentication

`POST /payments` and `GET /payments/{id}` need `Authorization: Bearer <PAYMENT_API_KEY>`. Without it they return `401`.
`POST /payments/{id}/confirm` is called from the browser and is authorized by the payment's `client_secret` instead.

### Health

`GET /health` returns `200 {"status":"ok"}`.

### Create a payment

`POST /payments` request:

```json
{ "amount_cents": 4800, "currency": "USD", "description": "Order ord_123", "metadata": { "order_id": "ord_123" } }
```

`201` response:

```json
{
  "id": "pay_abc",
  "status": "requires_confirmation",
  "amount_cents": 4800,
  "currency": "USD",
  "description": "Order ord_123",
  "metadata": { "order_id": "ord_123" },
  "client_secret": "pay_abc_secret_xyz",
  "created": 1763550000
}
```

- Returns `400` when `amount_cents` is not a positive integer or `currency` is not `USD`.
- An optional `Idempotency-Key` header makes retries safe. A second request with the same key returns the original
  payment instead of creating a new one.

### Confirm a payment

`POST /payments/{id}/confirm` request: `{ "client_secret": "pay_abc_secret_xyz", "card_number": "4242 4242 4242 4242" }`.
Spaces in the card number are allowed.

`200` response:

```json
{ "id": "pay_abc", "status": "succeeded", "failure_reason": null, "card_last4": "4242", "amount_cents": 4800, "currency": "USD" }
```

- Returns `400` when the card number is not 16 digits after spaces are removed.
- Returns `401` for a wrong `client_secret` and `404` for an unknown payment.
- Returns `409` when the payment was already confirmed.
- After confirming, the payment service sends a webhook (section 5).

Test cards:

| Card number           | Result                                                                 |
| --------------------- | ---------------------------------------------------------------------- |
| `4242 4242 4242 4242` | `succeeded`                                                            |
| `4000 0000 0000 0002` | `failed`, `failure_reason: "card_declined"`                            |
| `4000 0000 0000 9995` | `failed`, `failure_reason: "insufficient_funds"`                       |
| `4000 0000 0000 0077` | `succeeded`, and the webhook is delivered **twice** (same event id)    |
| any other 16 digits   | `failed`, `failure_reason: "card_declined"`                            |

### Read a payment

`GET /payments/{id}` returns `200` with the payment. It never includes the full card number, and it may include
`card_last4`.

## 5. Webhooks

The payment service sends `POST` to `WEBHOOK_URL` after every confirmation:

```http
POST /webhooks/payments
Content-Type: application/json
X-Payment-Signature: t=1763550000,v1=<64 hex characters>
```

```json
{
  "id": "evt_123",
  "type": "payment.succeeded",
  "created": 1763550000,
  "data": {
    "payment_id": "pay_abc",
    "status": "succeeded",
    "amount_cents": 4800,
    "currency": "USD",
    "failure_reason": null,
    "metadata": { "order_id": "ord_123" }
  }
}
```

- `type` is `payment.succeeded` or `payment.failed`. Level 3 also has `payment.refunded`.
- `created` is when the event happened, in Unix seconds.

### Signature

```text
v1 = hex( HMAC_SHA256( PAYMENT_WEBHOOK_SECRET, "<t>.<raw request body>" ) )
```

The backend must:

- Verify the signature against the exact bytes it received.
- Reject a request with a `4xx` (we suggest `401`) when the header is missing or malformed, the signature does not match,
  or `t` is more than 300 seconds from the current time.
- Answer `2xx` to every validly signed event, including event types or payments it does not recognise, so the provider
  stops retrying.

### Delivery

Like real providers, the payment service delivers each event **at least once** and **not necessarily in order**. The
backend must end up in the same state as if it had applied each event exactly once, in `created` order. The backend may
also call `GET /payments/{id}` to double-check a payment.

When the backend answers with a non-`2xx` status or times out, the payment service retries at least 3 times with backoff.

## 6. Frontend

Any framework works, and the design can be plain. A person must be able to do all of this in a browser:

1. **Shop.** See the catalog with name, price (for example `$24.00`) and stock, and add products to a cart.
2. **Checkout.** Enter an email and a test card, pay, and see whether the order was paid or declined.
3. **Subscribe.** Pick a plan, enter an email and a test card, and watch the subscription status change to `active`.
4. **Admin.** Add a product, restock a product, and see the import report.

The card number goes from the browser straight to the payment service. Enable CORS on the backend and the payment
service for the frontend's origin, or proxy both through the frontend server.

## 7. Level 3: migrate the old store's subscribers

Pinecone is leaving its old store and payment provider. Two exports describe the existing subscribers:

- `data/customers.csv` is the customer export from the old store.
- `data/legacy_payment_events.jsonl` is the old provider's webhook delivery log, one delivery per line. `body` is the raw
  request body as received. The header `x-legacy-signature` uses the same scheme as section 5, signed with
  `LEGACY_WEBHOOK_SECRET`. The 300-second tolerance does not apply to these historical events. Legacy event bodies carry
  `customer_email` and `plan_id` instead of `metadata`.

Add `POST /admin/migrations/legacy`. It imports the customers, replays the events under the rules in sections 3 and 5,
and returns a report:

```json
{
  "customers": { "rows_read": 0, "imported": 0, "rejected": [{ "line": 3, "reason": "..." }] },
  "events": { "deliveries_read": 0, "applied": 0, "skipped": [{ "delivery_id": "dlv_0001", "reason": "..." }] }
}
```

- `applied + skipped.length` must equal `deliveries_read`.
- Running the migration twice must leave the same state as running it once.
- Afterwards, `GET /customers/{email}/subscription` must return each existing customer's correct current status.

## 8. Errors

Every error response uses this shape:

```json
{ "error": { "code": "insufficient_inventory", "message": "Only 3 left of TEE-CLASSIC-BLK" } }
```

The tests check status codes, not `code` values.
