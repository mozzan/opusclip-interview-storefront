import { test } from 'node:test';
import assert from 'node:assert/strict';
import { backend, PLAN_RECORDS, PRODUCT_RECORDS } from '../lib/helpers.mjs';

const SKU_PATTERN = /^[A-Z0-9-]+$/;

function assertValidProduct(p) {
  const label = `product ${JSON.stringify(p?.sku)}`;
  assert.equal(typeof p.sku, 'string', `${label}: sku must be a string`);
  assert.match(p.sku, SKU_PATTERN, `${label}: sku must be canonical (A-Z, 0-9, -)`);
  assert.equal(typeof p.name, 'string', `${label}: name must be a string`);
  assert.ok(p.name.trim().length > 0, `${label}: name must not be empty`);
  assert.ok(Number.isInteger(p.price_cents) && p.price_cents > 0, `${label}: price_cents must be a positive integer, got ${p.price_cents}`);
  assert.equal(p.currency, 'USD', `${label}: currency must be USD, got ${p.currency}`);
  assert.ok(Number.isInteger(p.inventory) && p.inventory >= 0, `${label}: inventory must be an integer >= 0, got ${p.inventory}`);
}

test('L1 GET /products lists the catalog loaded from data/products.csv', async () => {
  const res = await backend('GET', '/products');
  assert.equal(res.status, 200);
  assert.ok(Array.isArray(res.json), 'expected a JSON array');
  assert.ok(res.json.length >= 25, `expected at least 25 products from data/products.csv, got ${res.json.length}`);
});

test('L1 every listed product follows the product rules in SPEC.md section 2', async () => {
  const res = await backend('GET', '/products');
  for (const p of res.json) assertValidProduct(p);
});

test('L1 listed SKUs are unique', async () => {
  const res = await backend('GET', '/products');
  const seen = new Map();
  for (const p of res.json) {
    const key = p.sku.trim().toUpperCase();
    assert.ok(!seen.has(key), `SKU ${key} is listed more than once`);
    seen.set(key, p);
  }
});

test('L1 GET /products/{sku} finds a product without caring about case', async () => {
  const list = (await backend('GET', '/products')).json;
  const sample = list[0];
  const exact = await backend('GET', `/products/${encodeURIComponent(sample.sku)}`);
  assert.equal(exact.status, 200);
  assert.equal(exact.json.sku, sample.sku);
  const lower = await backend('GET', `/products/${encodeURIComponent(sample.sku.toLowerCase())}`);
  assert.equal(lower.status, 200);
  assert.equal(lower.json.sku, sample.sku);
  const missing = await backend('GET', '/products/NO-SUCH-SKU-000');
  assert.equal(missing.status, 404);
});

test('L1 GET /plans lists the plans that can be bought', async () => {
  const res = await backend('GET', '/plans');
  assert.equal(res.status, 200);
  assert.ok(Array.isArray(res.json) && res.json.length > 0, 'expected a non-empty JSON array');
  for (const plan of res.json) {
    const label = `plan ${JSON.stringify(plan?.plan_id)}`;
    assert.equal(typeof plan.plan_id, 'string', `${label}: plan_id`);
    assert.equal(typeof plan.name, 'string', `${label}: name`);
    assert.ok(Number.isInteger(plan.price_cents) && plan.price_cents > 0, `${label}: price_cents must be a positive integer, got ${plan.price_cents}`);
    assert.equal(plan.currency, 'USD', `${label}: currency`);
    assert.ok(['month', 'year'].includes(plan.interval), `${label}: interval must be month or year, got ${plan.interval}`);
  }
  const basic = res.json.find((p) => p.plan_id === 'basic-monthly');
  assert.ok(basic, 'basic-monthly should be on sale');
  assert.equal(basic.price_cents, 900);
});

function assertReportSection(section, expectedRows, name) {
  assert.ok(section, `import report is missing "${name}"`);
  assert.equal(section.rows_read, expectedRows, `${name}.rows_read should be the number of data records in data/${name}.csv`);
  assert.ok(Number.isInteger(section.imported), `${name}.imported must be an integer`);
  assert.ok(Array.isArray(section.rejected), `${name}.rejected must be an array`);
  assert.equal(
    section.imported + section.rejected.length,
    section.rows_read,
    `${name}: imported (${section.imported}) + rejected (${section.rejected.length}) must equal rows_read (${section.rows_read})`,
  );
  for (const r of section.rejected) {
    assert.equal(typeof r.reason, 'string', `${name}: every rejected row needs a reason`);
    assert.ok(r.reason.trim().length > 0, `${name}: every rejected row needs a reason`);
  }
}

test('L1 GET /admin/import-report accounts for every row in the data files', async () => {
  const res = await backend('GET', '/admin/import-report');
  assert.equal(res.status, 200);
  assertReportSection(res.json?.products, PRODUCT_RECORDS, 'products');
  assertReportSection(res.json?.plans, PLAN_RECORDS, 'plans');
});
