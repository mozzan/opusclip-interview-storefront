import { test } from 'node:test';
import assert from 'node:assert/strict';
import { backend, CUSTOMER_RECORDS, LEGACY_DELIVERIES } from '../lib/helpers.mjs';

const migrate = () => backend('POST', '/admin/migrations/legacy');
const subscriptionOf = (email) => backend('GET', `/customers/${encodeURIComponent(email)}/subscription`);

function assertReport(report) {
  assert.ok(report?.customers && report?.events, 'report needs "customers" and "events"');
  const { customers, events } = report;
  assert.equal(customers.rows_read, CUSTOMER_RECORDS, 'customers.rows_read');
  assert.equal(customers.imported + customers.rejected.length, customers.rows_read, 'customers: imported + rejected = rows_read');
  assert.equal(events.deliveries_read, LEGACY_DELIVERIES, 'events.deliveries_read');
  assert.equal(events.applied + events.skipped.length, events.deliveries_read, 'events: applied + skipped = deliveries_read');
  for (const s of events.skipped) assert.ok(typeof s.reason === 'string' && s.reason.trim(), 'every skipped delivery needs a reason');
}

test('L3 POST /admin/migrations/legacy imports the old store and reports on every row', async (t) => {
  const res = await migrate();
  if (res.status === 404) return t.skip('migration endpoint not implemented');
  assert.equal(res.status, 200, res.text);
  assertReport(res.json);
});

test('L3 migrated customers get their subscription status', async (t) => {
  // The migration must be safe to repeat, so run it here too in case this test runs on its own.
  const res = await migrate();
  if (res.status === 404) return t.skip('migration endpoint not implemented');
  assert.equal(res.status, 200, res.text);

  const avery = await subscriptionOf('avery.chen@example.com');
  assert.equal(avery.json.status, 'active');
  assert.equal(avery.json.plan_id, 'pro-monthly');

  const milo = await subscriptionOf('milo.rossi@example.com');
  assert.equal(milo.json.status, 'canceled', 'paid, then refunded');

  const rae = await subscriptionOf('rae.kim@example.com');
  assert.notEqual(rae.json.status, 'active', 'never subscribed');
});

test('L3 running the migration again changes nothing', async (t) => {
  const before = await Promise.all(['avery.chen@example.com', 'milo.rossi@example.com', 'nora.quinn@example.com'].map(subscriptionOf));
  const res = await migrate();
  if (res.status === 404) return t.skip('migration endpoint not implemented');
  assert.equal(res.status, 200, res.text);
  assertReport(res.json);
  const after = await Promise.all(['avery.chen@example.com', 'milo.rossi@example.com', 'nora.quinn@example.com'].map(subscriptionOf));
  // Compare only what the spec defines, so extra fields such as ids or timestamps don't matter.
  const state = (r) => ({ status: r.json?.status, plan_id: r.json?.plan_id });
  assert.deepEqual(after.map(state), before.map(state));
});
