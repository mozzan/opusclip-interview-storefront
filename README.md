# Pinecone Supply Co.: storefront and checkout exercise

Pinecone Supply Co. is a small outdoor-goods brand. It sells products and a membership subscription, and it is moving
off its old store and payment provider. Your job is to build the new one as three small services:

```text
 browser ──► frontend :3000 ──► backend :4000 ◄── signed webhooks ── payment-service :4242
    │                               │                                       ▲
    └───────────── card number ─────┼───────────────────────────────────────┘
                                    └── create / read payments (API key) ────┘
```

- **frontend**: shop, cart, checkout, subscribe, and admin pages.
- **backend**: products, inventory, plans, orders, subscriptions, and a webhook receiver.
- **payment-service**: a fake payment processor. It confirms test cards and sends signed webhooks. There is no Stripe and
  no real money.

[`SPEC.md`](SPEC.md) is the contract. [`data/`](data/) holds the business's real exports. [`tests/`](tests/) holds the
acceptance tests that decide whether a level passes.

## How we work in this exercise

We expect you to build this with an AI coding agent, such as Claude Code, Cursor, Codex or Copilot. Use whichever one you
are most productive with. We care about how you direct the agent, how you check what it gives you, and the decisions you
make along the way. Typing speed does not matter.

- **Pick any stack.** Any language, framework or store works. In-memory storage is fine.
- **Treat `data/` and `tests/` as read-only.** They are the inputs and the acceptance criteria.
- **Start everything with one command,** for example `docker compose up` or `npm run dev`. Write it in `SOLUTION.md`.
- **The interviewer plays the store owner.** Ask them anything about the business, the data or the requirements.
- **Think aloud and share your screen,** including the agent's conversation.

Suggested time: about 2 hours of building, then a 15-minute demo and discussion. Most people do not finish Level 3, and
that is fine.

## Levels

| Level | Expectation   | What it covers                                                                                              |
| ----- | ------------- | ----------------------------------------------------------------------------------------------------------- |
| **L1** | Must pass    | Catalog loaded from `data/`, import report, add product and restock, payments, signed webhooks, order checkout, subscription checkout, frontend up |
| **L2** | Should pass  | Idempotency keys, duplicate webhook delivery, stale signatures                                              |
| **L3** | Stretch      | Migrate the old store's subscribers from `data/customers.csv` and `data/legacy_payment_events.jsonl`        |

## Running the acceptance tests

The tests are black-box HTTP tests. They need Node.js 20.10 or later and no `npm install`. Start your three services,
then run:

```bash
npm test
```

Run one level at a time:

```bash
npm run test:l1
```

`npm run test:l2` and `npm run test:l3` work the same way.

When your services run somewhere other than the defaults:

```bash
FRONTEND_URL=http://localhost:3000 BACKEND_URL=http://localhost:4000 PAYMENT_URL=http://localhost:4242 npm test
```

The tests create their own products and customers with random IDs, so you can run them as often as you like against
the same running services.

## What to show at the end

1. `npm test` output.
2. In the browser, add a product in admin and see it appear in the shop.
3. Buy it with `4242 4242 4242 4242`. The order is paid and the stock goes down.
4. Try `4000 0000 0000 0002`. The payment is declined and the stock does not change.
5. Subscribe to a plan. The status goes from `pending` to `active`.
6. Open the import report and walk us through it.
7. Walk us through `SOLUTION.md`.

## Repository layout

```text
SPEC.md            the contract: APIs, data rules, webhook signing
SOLUTION.md        your notes (fill this in)
data/              exports from the business (read-only)
tests/acceptance/  acceptance tests (read-only)
services/          put your three services here
```
