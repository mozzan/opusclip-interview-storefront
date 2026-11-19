# Data

These files are real exports from the business. Do not edit them. [`SPEC.md`](../SPEC.md) section 2 has the field rules.

| File                          | Source                                                                     | Used in |
| ----------------------------- | -------------------------------------------------------------------------- | ------- |
| `products.csv`                | Catalog export from the merchandising team, taken 2026-11-02.              | L1      |
| `plans.csv`                   | Membership plans, maintained by finance.                                   | L1      |
| `customers.csv`               | Customer export from the old store.                                        | L3      |
| `legacy_payment_events.jsonl` | Webhook delivery log exported from the old payment provider.               | L3      |

## Formats

`products.csv`, `plans.csv` and `customers.csv` are UTF-8 CSV files with a header row. Fields may be quoted.

`legacy_payment_events.jsonl` holds one JSON object per line:

| Field         | Meaning                                                              |
| ------------- | -------------------------------------------------------------------- |
| `delivery_id` | The old provider's ID for this delivery attempt.                     |
| `received_at` | When the old store received the delivery.                            |
| `headers`     | The request headers that matter, including `x-legacy-signature`.     |
| `body`        | The raw request body, as a string, exactly as received.              |
