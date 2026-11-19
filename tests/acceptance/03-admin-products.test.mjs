import { test } from 'node:test';
import assert from 'node:assert/strict';
import { backend, createProduct, inventoryOf, testSku } from '../lib/helpers.mjs';

test('L1 POST /products adds a product and returns it in canonical form', async () => {
  const sku = testSku();
  const res = await backend('POST', '/products', {
    body: { sku: `  ${sku.toLowerCase()} `, name: 'Admin Test Mug', price_cents: 1800, currency: 'USD', inventory: 25 },
  });
  assert.equal(res.status, 201, res.text);
  assert.equal(res.json.sku, sku);
  assert.equal(res.json.price_cents, 1800);
  assert.equal(res.json.inventory, 25);

  const one = await backend('GET', `/products/${sku}`);
  assert.equal(one.status, 200);
  assert.equal(one.json.name, 'Admin Test Mug');

  const list = await backend('GET', '/products');
  assert.ok(list.json.some((p) => p.sku === sku), 'new product should be listed in GET /products');
});

test('L1 POST /products refuses a SKU that already exists, whatever its case', async () => {
  const product = await createProduct();
  const again = await backend('POST', '/products', {
    body: { sku: product.sku.toLowerCase(), name: 'Duplicate', price_cents: 1000, currency: 'USD', inventory: 1 },
  });
  assert.equal(again.status, 409, again.text);
});

const INVALID_PRODUCTS = [
  ['missing sku', { name: 'X', price_cents: 1000, currency: 'USD', inventory: 1 }],
  ['sku with spaces inside', { sku: 'BAD SKU', name: 'X', price_cents: 1000, currency: 'USD', inventory: 1 }],
  ['missing name', { price_cents: 1000, currency: 'USD', inventory: 1 }],
  ['blank name', { name: '   ', price_cents: 1000, currency: 'USD', inventory: 1 }],
  ['zero price', { name: 'X', price_cents: 0, currency: 'USD', inventory: 1 }],
  ['negative price', { name: 'X', price_cents: -100, currency: 'USD', inventory: 1 }],
  ['fractional price', { name: 'X', price_cents: 12.5, currency: 'USD', inventory: 1 }],
  ['price as a string', { name: 'X', price_cents: '1200', currency: 'USD', inventory: 1 }],
  ['non-USD currency', { name: 'X', price_cents: 1000, currency: 'EUR', inventory: 1 }],
  ['negative inventory', { name: 'X', price_cents: 1000, currency: 'USD', inventory: -1 }],
  ['fractional inventory', { name: 'X', price_cents: 1000, currency: 'USD', inventory: 1.5 }],
];

for (const [label, body] of INVALID_PRODUCTS) {
  test(`L1 POST /products rejects a product with ${label} (400)`, async () => {
    const payload = 'sku' in body || label === 'missing sku' ? body : { sku: testSku(), ...body };
    const res = await backend('POST', '/products', { body: payload });
    assert.equal(res.status, 400, `expected 400 for ${label}, got ${res.status}: ${res.text}`);
  });
}

test('L1 POST /products/{sku}/restock adds stock', async () => {
  const product = await createProduct({ inventory: 3 });
  const res = await backend('POST', `/products/${product.sku}/restock`, { body: { quantity: 5 } });
  assert.equal(res.status, 200, res.text);
  assert.equal(res.json.inventory, 8);
  assert.equal(await inventoryOf(product.sku), 8);
});

test('L1 POST /products/{sku}/restock rejects bad quantities and unknown SKUs', async () => {
  const product = await createProduct({ inventory: 3 });
  for (const quantity of [0, -3, 2.5, '4']) {
    const res = await backend('POST', `/products/${product.sku}/restock`, { body: { quantity } });
    assert.equal(res.status, 400, `quantity ${JSON.stringify(quantity)} should be rejected, got ${res.status}`);
  }
  assert.equal(await inventoryOf(product.sku), 3, 'rejected restocks must not change inventory');
  const unknown = await backend('POST', '/products/NO-SUCH-SKU-000/restock', { body: { quantity: 1 } });
  assert.equal(unknown.status, 404);
});
