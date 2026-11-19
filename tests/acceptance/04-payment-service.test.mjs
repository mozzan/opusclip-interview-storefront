import { test } from 'node:test';
import assert from 'node:assert/strict';
import { call, CARDS, confirmPayment, payments, PAYMENT_URL, uid } from '../lib/helpers.mjs';

const newPayment = (overrides = {}, headers = {}) =>
  payments('POST', '/payments', {
    body: { amount_cents: 4800, currency: 'USD', description: 'acceptance test', metadata: { test_run: uid() }, ...overrides },
    headers,
  });

test('L1 POST /payments needs the API key', async () => {
  const noKey = await call(PAYMENT_URL, 'POST', '/payments', { body: { amount_cents: 100, currency: 'USD' } });
  assert.equal(noKey.status, 401);
  const wrongKey = await call(PAYMENT_URL, 'POST', '/payments', {
    body: { amount_cents: 100, currency: 'USD' },
    headers: { authorization: 'Bearer sk_wrong' },
  });
  assert.equal(wrongKey.status, 401);
});

test('L1 POST /payments creates a payment that needs confirmation', async () => {
  const metadata = { order_id: `ord_${uid()}` };
  const res = await newPayment({ metadata });
  assert.equal(res.status, 201, res.text);
  assert.equal(typeof res.json.id, 'string');
  assert.equal(res.json.status, 'requires_confirmation');
  assert.equal(res.json.amount_cents, 4800);
  assert.equal(res.json.currency, 'USD');
  assert.deepEqual(res.json.metadata, metadata);
  assert.equal(typeof res.json.client_secret, 'string');
});

test('L1 POST /payments rejects bad amounts and currencies (400)', async () => {
  for (const body of [{ amount_cents: 0 }, { amount_cents: -5 }, { amount_cents: 10.5 }, { amount_cents: '100' }, { currency: 'EUR' }]) {
    const res = await newPayment(body);
    assert.equal(res.status, 400, `${JSON.stringify(body)} should be rejected, got ${res.status}`);
  }
});

test('L1 confirming with the success card succeeds, and the full card number is never returned', async () => {
  const payment = (await newPayment()).json;
  const res = await confirmPayment(payment, CARDS.success);
  assert.equal(res.status, 200, res.text);
  assert.equal(res.json.status, 'succeeded');
  assert.equal(res.json.card_last4, '4242');

  const read = await payments('GET', `/payments/${payment.id}`);
  assert.equal(read.status, 200);
  assert.equal(read.json.status, 'succeeded');
  assert.ok(!read.text.replace(/\s/g, '').includes('4242424242424242'), 'GET /payments/{id} must not return the full card number');
});

test('L1 the declined and insufficient-funds cards fail with the right reason', async () => {
  const declined = await confirmPayment((await newPayment()).json, CARDS.declined);
  assert.equal(declined.status, 200, declined.text);
  assert.equal(declined.json.status, 'failed');
  assert.equal(declined.json.failure_reason, 'card_declined');

  const broke = await confirmPayment((await newPayment()).json, CARDS.insufficientFunds);
  assert.equal(broke.status, 200, broke.text);
  assert.equal(broke.json.status, 'failed');
  assert.equal(broke.json.failure_reason, 'insufficient_funds');
});

test('L1 confirm rejects a wrong client secret, a bad card number, an unknown payment, and a second confirmation', async () => {
  const payment = (await newPayment()).json;
  const wrongSecret = await confirmPayment({ ...payment, client_secret: 'nope' }, CARDS.success);
  assert.equal(wrongSecret.status, 401, wrongSecret.text);

  const badCard = await confirmPayment(payment, '1234');
  assert.equal(badCard.status, 400, badCard.text);

  const unknown = await confirmPayment({ id: 'pay_does_not_exist', client_secret: 'x' }, CARDS.success);
  assert.equal(unknown.status, 404, unknown.text);

  const first = await confirmPayment(payment, CARDS.success);
  assert.equal(first.status, 200, first.text);
  const second = await confirmPayment(payment, CARDS.success);
  assert.equal(second.status, 409, second.text);
});

test('L2 POST /payments with the same Idempotency-Key returns the original payment', async () => {
  // A retry sends the identical request body with the same key.
  const key = `idem-${uid()}`;
  const body = { amount_cents: 1234, metadata: { test_run: uid() } };
  const a = await newPayment(body, { 'idempotency-key': key });
  const b = await newPayment(body, { 'idempotency-key': key });
  assert.ok([200, 201].includes(a.status) && [200, 201].includes(b.status), `${a.status} / ${b.status}`);
  assert.equal(b.json.id, a.json.id, 'same Idempotency-Key must return the same payment');
  const c = await newPayment(body, { 'idempotency-key': `idem-${uid()}` });
  assert.notEqual(c.json.id, a.json.id, 'a different key must create a new payment');
});
