import { test } from 'node:test';
import assert from 'node:assert/strict';
import { backend, call, FRONTEND_URL, PAYMENT_URL } from '../lib/helpers.mjs';

test('L1 backend answers GET /health', async () => {
  const res = await backend('GET', '/health');
  assert.equal(res.status, 200);
  assert.equal(res.json?.status, 'ok');
});

test('L1 payment service answers GET /health', async () => {
  const res = await call(PAYMENT_URL, 'GET', '/health');
  assert.equal(res.status, 200);
  assert.equal(res.json?.status, 'ok');
});

test('L1 frontend serves an HTML page at /', async () => {
  const res = await call(FRONTEND_URL, 'GET', '/');
  assert.equal(res.status, 200);
  assert.match(res.headers.get('content-type') ?? '', /text\/html/);
});
