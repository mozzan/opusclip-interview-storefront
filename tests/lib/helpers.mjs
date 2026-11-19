// Shared helpers for the acceptance tests. No dependencies: Node 20.10+ only.
import { createHmac, randomBytes } from 'node:crypto';

export const FRONTEND_URL = process.env.FRONTEND_URL ?? 'http://localhost:3000';
export const BACKEND_URL = process.env.BACKEND_URL ?? 'http://localhost:4000';
export const PAYMENT_URL = process.env.PAYMENT_URL ?? 'http://localhost:4242';
export const PAYMENT_API_KEY = process.env.PAYMENT_API_KEY ?? 'sk_test_pinecone_local';
export const PAYMENT_WEBHOOK_SECRET = process.env.PAYMENT_WEBHOOK_SECRET ?? 'whsec_test_pinecone_local';

// Number of data records (not lines) in the files in data/, header excluded.
export const PRODUCT_RECORDS = 49;
export const PLAN_RECORDS = 5;
export const CUSTOMER_RECORDS = 19;
export const LEGACY_DELIVERIES = 24;

export const CARDS = {
  success: '4242 4242 4242 4242',
  declined: '4000 0000 0000 0002',
  insufficientFunds: '4000 0000 0000 9995',
  duplicateDelivery: '4000 0000 0000 0077',
};

/** Make an HTTP call and return { status, json, text, headers }. Never throws on non-2xx. */
export async function call(base, method, path, { body, raw, headers = {} } = {}) {
  const init = { method, headers: { ...headers } };
  if (raw !== undefined) {
    init.body = raw;
    init.headers['content-type'] ??= 'application/json';
  } else if (body !== undefined) {
    init.body = JSON.stringify(body);
    init.headers['content-type'] = 'application/json';
  }
  let res;
  try {
    res = await fetch(base + path, init);
  } catch (err) {
    throw new Error(`${method} ${base}${path} failed: ${err.cause?.code ?? err.message}. Is the service running?`);
  }
  const text = await res.text();
  let json;
  try {
    json = text ? JSON.parse(text) : undefined;
  } catch {
    json = undefined;
  }
  return { status: res.status, json, text, headers: res.headers };
}

export const backend = (method, path, opts) => call(BACKEND_URL, method, path, opts);

/** Payment service call with the secret API key (what the backend does). */
export const payments = (method, path, opts = {}) =>
  call(PAYMENT_URL, method, path, {
    ...opts,
    headers: { authorization: `Bearer ${PAYMENT_API_KEY}`, ...(opts.headers ?? {}) },
  });

/** Confirm a payment the way the browser does: client secret, no API key. */
export const confirmPayment = (payment, cardNumber) =>
  call(PAYMENT_URL, 'POST', `/payments/${payment.id}/confirm`, {
    body: { client_secret: payment.client_secret, card_number: cardNumber },
  });

export function uid() {
  return randomBytes(5).toString('hex');
}

export function testSku() {
  return `TEST-${uid().toUpperCase()}`;
}

export function testEmail(prefix = 'test') {
  return `${prefix}-${uid()}@example.com`;
}

/** Build an X-Payment-Signature header value for a raw body. */
export function signature(rawBody, { secret = PAYMENT_WEBHOOK_SECRET, t = Math.floor(Date.now() / 1000) } = {}) {
  const v1 = createHmac('sha256', secret).update(`${t}.${rawBody}`).digest('hex');
  return `t=${t},v1=${v1}`;
}

export function sendWebhook(rawBody, signatureHeader) {
  const headers = { 'content-type': 'application/json' };
  if (signatureHeader !== undefined) headers['x-payment-signature'] = signatureHeader;
  return backend('POST', '/webhooks/payments', { raw: rawBody, headers });
}

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** Poll fn() until predicate(result) is true, or fail after timeoutMs with the last result. */
export async function waitFor(fn, predicate, { timeoutMs = 10_000, intervalMs = 200, what = 'condition' } = {}) {
  const deadline = Date.now() + timeoutMs;
  let last;
  while (Date.now() < deadline) {
    last = await fn();
    if (predicate(last)) return last;
    await sleep(intervalMs);
  }
  throw new Error(`Timed out after ${timeoutMs} ms waiting for ${what}. Last value: ${JSON.stringify(last?.json ?? last)}`);
}

/** Create a product through the admin API and return it. */
export async function createProduct(overrides = {}) {
  const body = {
    sku: testSku(),
    name: 'Acceptance Test Product',
    description: 'Created by the acceptance tests',
    category: 'test',
    price_cents: 1500,
    currency: 'USD',
    inventory: 10,
    ...overrides,
  };
  const res = await backend('POST', '/products', { body });
  if (res.status !== 201) throw new Error(`POST /products returned ${res.status}: ${res.text}`);
  return res.json;
}

export async function inventoryOf(sku) {
  const res = await backend('GET', `/products/${encodeURIComponent(sku)}`);
  if (res.status !== 200) throw new Error(`GET /products/${sku} returned ${res.status}: ${res.text}`);
  return res.json.inventory;
}

export const isClientError = (status) => status >= 400 && status < 500;
