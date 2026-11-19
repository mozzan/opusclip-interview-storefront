import { test } from 'node:test';
import assert from 'node:assert/strict';
import { backend, CARDS, confirmPayment, createProduct, inventoryOf, sleep, testEmail, waitFor } from '../lib/helpers.mjs';

const orderStatus = (orderId) => backend('GET', `/orders/${orderId}`);
const settled = (res) => res.status === 200 && res.json?.status !== 'pending_payment';

async function placeOrder(sku, quantity, extra = {}) {
  const res = await backend('POST', '/orders', {
    body: { customer_email: testEmail('buyer'), items: [{ sku, quantity, ...extra }] },
  });
  assert.equal(res.status, 201, `POST /orders: ${res.status} ${res.text}`);
  return res.json;
}

test('L1 POST /orders prices the order on the server', async () => {
  const product = await createProduct({ price_cents: 1500, inventory: 10 });
  const order = await placeOrder(product.sku, 3, { unit_price_cents: 1, price_cents: 1 });
  assert.equal(order.status, 'pending_payment');
  assert.equal(order.amount_cents, 4500, 'amount must come from the catalog price, not the client');
  assert.equal(order.currency, 'USD');
  assert.equal(typeof order.payment?.id, 'string');
  assert.equal(typeof order.payment?.client_secret, 'string');
  assert.equal(await inventoryOf(product.sku), 10, 'creating an order must not change inventory');
});

test('L1 a paid order reduces inventory', async () => {
  const product = await createProduct({ price_cents: 1500, inventory: 10 });
  const order = await placeOrder(product.sku, 3);
  const confirm = await confirmPayment(order.payment, CARDS.success);
  assert.equal(confirm.json?.status, 'succeeded', confirm.text);

  const final = await waitFor(() => orderStatus(order.order_id), settled, { what: 'order to settle' });
  assert.equal(final.json.status, 'paid');
  assert.equal(await inventoryOf(product.sku), 7);
});

test('L1 a declined payment marks the order payment_failed and leaves inventory alone', async () => {
  const product = await createProduct({ price_cents: 1500, inventory: 10 });
  const order = await placeOrder(product.sku, 2);
  await confirmPayment(order.payment, CARDS.declined);

  const final = await waitFor(() => orderStatus(order.order_id), settled, { what: 'order to settle' });
  assert.equal(final.json.status, 'payment_failed');
  assert.equal(await inventoryOf(product.sku), 10);
});

test('L1 POST /orders refuses more than the inventory (409)', async () => {
  const product = await createProduct({ inventory: 2 });
  const res = await backend('POST', '/orders', {
    body: { customer_email: testEmail('buyer'), items: [{ sku: product.sku, quantity: 3 }] },
  });
  assert.equal(res.status, 409, res.text);
});

test('L1 POST /orders validates the request', async () => {
  const product = await createProduct();
  const cases = [
    [400, { customer_email: 'not-an-email', items: [{ sku: product.sku, quantity: 1 }] }],
    [400, { customer_email: testEmail(), items: [] }],
    [400, { customer_email: testEmail(), items: [{ sku: product.sku, quantity: 0 }] }],
    [400, { customer_email: testEmail(), items: [{ sku: product.sku, quantity: 1.5 }] }],
    [404, { customer_email: testEmail(), items: [{ sku: 'NO-SUCH-SKU-000', quantity: 1 }] }],
  ];
  for (const [expected, body] of cases) {
    const res = await backend('POST', '/orders', { body });
    assert.equal(res.status, expected, `${JSON.stringify(body)}: expected ${expected}, got ${res.status}`);
  }
});

test('L1 the storefront can find an order it created', async () => {
  const product = await createProduct();
  const order = await placeOrder(product.sku, 1);
  const res = await orderStatus(order.order_id);
  assert.equal(res.status, 200);
  assert.equal(res.json.order_id, order.order_id);
  assert.equal(res.json.amount_cents, order.amount_cents);
  const missing = await orderStatus('ord_does_not_exist');
  assert.equal(missing.status, 404);
});

test('L2 a webhook delivered twice reduces inventory once', async () => {
  const product = await createProduct({ price_cents: 1000, inventory: 10 });
  const order = await placeOrder(product.sku, 2);
  await confirmPayment(order.payment, CARDS.duplicateDelivery);

  const final = await waitFor(() => orderStatus(order.order_id), settled, { what: 'order to settle' });
  assert.equal(final.json.status, 'paid');
  await sleep(2000); // let the second delivery land
  assert.equal(await inventoryOf(product.sku), 8, 'the duplicate delivery must not take stock a second time');
});
