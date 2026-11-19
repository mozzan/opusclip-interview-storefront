import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isClientError, sendWebhook, signature, uid } from '../lib/helpers.mjs';

// Events about payments the backend has never seen: a correct receiver acknowledges them and changes nothing.
function unknownPaymentEvent() {
  return {
    id: `evt_test_${uid()}`,
    type: 'payment.succeeded',
    created: Math.floor(Date.now() / 1000),
    data: {
      payment_id: `pay_unknown_${uid()}`,
      status: 'succeeded',
      amount_cents: 100,
      currency: 'USD',
      failure_reason: null,
      metadata: {},
    },
  };
}

test('L1 webhook without a signature header is rejected', async () => {
  const raw = JSON.stringify(unknownPaymentEvent());
  const res = await sendWebhook(raw, undefined);
  assert.ok(isClientError(res.status), `expected 4xx, got ${res.status}`);
});

test('L1 webhook with a malformed or wrong signature is rejected', async () => {
  const raw = JSON.stringify(unknownPaymentEvent());
  for (const header of ['garbage', 't=123', `t=${Math.floor(Date.now() / 1000)},v1=${'0'.repeat(64)}`, signature(raw, { secret: 'whsec_wrong' })]) {
    const res = await sendWebhook(raw, header);
    assert.ok(isClientError(res.status), `header ${JSON.stringify(header)}: expected 4xx, got ${res.status}`);
  }
});

test('L1 webhook whose body was changed after signing is rejected', async () => {
  const event = unknownPaymentEvent();
  const signed = JSON.stringify(event);
  const tampered = JSON.stringify({ ...event, data: { ...event.data, amount_cents: 1 } });
  const res = await sendWebhook(tampered, signature(signed));
  assert.ok(isClientError(res.status), `expected 4xx, got ${res.status}`);
});

test('L1 correctly signed webhook for an unknown payment is acknowledged (2xx)', async () => {
  const raw = JSON.stringify(unknownPaymentEvent());
  const res = await sendWebhook(raw, signature(raw));
  assert.ok(res.status >= 200 && res.status < 300, `expected 2xx, got ${res.status}: ${res.text}`);
});

test('L1 signature is checked against the raw body, not re-serialized JSON', async () => {
  // Same event, but pretty-printed. The signature covers these exact bytes, so it must be accepted.
  const raw = JSON.stringify(unknownPaymentEvent(), null, 2) + '\n';
  const res = await sendWebhook(raw, signature(raw));
  assert.ok(res.status >= 200 && res.status < 300, `expected 2xx for a correctly signed pretty-printed body, got ${res.status}: ${res.text}`);
});

test('L2 webhook signed more than 300 seconds ago is rejected', async () => {
  const raw = JSON.stringify(unknownPaymentEvent());
  const stale = signature(raw, { t: Math.floor(Date.now() / 1000) - 600 });
  const res = await sendWebhook(raw, stale);
  assert.ok(isClientError(res.status), `expected 4xx for a stale timestamp, got ${res.status}`);
});
