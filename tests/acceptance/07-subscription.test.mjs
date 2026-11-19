import { test } from 'node:test';
import assert from 'node:assert/strict';
import { backend, CARDS, confirmPayment, sleep, testEmail, waitFor } from '../lib/helpers.mjs';

const subscriptionOf = (email) => backend('GET', `/customers/${encodeURIComponent(email)}/subscription`);
const notPending = (res) => res.status === 200 && res.json?.status !== 'pending';

async function subscribe(email, planId = 'basic-monthly') {
  const res = await backend('POST', '/subscriptions', { body: { customer_email: email, plan_id: planId } });
  assert.equal(res.status, 201, `POST /subscriptions: ${res.status} ${res.text}`);
  return res.json;
}

test('L1 a new customer has no subscription', async () => {
  const res = await subscriptionOf(testEmail('nobody'));
  assert.equal(res.status, 200);
  assert.equal(res.json.status, 'none');
  assert.equal(res.json.plan_id, null);
});

test('L1 subscribing and paying makes the subscription active', async () => {
  const email = testEmail('member');
  const sub = await subscribe(email);
  assert.equal(sub.status, 'pending');
  assert.equal(sub.plan_id, 'basic-monthly');
  assert.equal(sub.amount_cents, 900);
  assert.equal(sub.currency, 'USD');

  const before = await subscriptionOf(email);
  assert.equal(before.json.status, 'pending');

  const confirm = await confirmPayment(sub.payment, CARDS.success);
  assert.equal(confirm.json?.status, 'succeeded', confirm.text);

  const after = await waitFor(() => subscriptionOf(email), notPending, { what: 'subscription to settle' });
  assert.equal(after.json.status, 'active');
  assert.equal(after.json.plan_id, 'basic-monthly');
});

test('L1 customer emails are not case-sensitive', async () => {
  const email = testEmail('MixedCase');
  const sub = await subscribe(email.toUpperCase());
  await confirmPayment(sub.payment, CARDS.success);
  const after = await waitFor(() => subscriptionOf(email.toLowerCase()), notPending, { what: 'subscription to settle' });
  assert.equal(after.json.status, 'active');
});

test('L1 an active customer cannot subscribe again (409)', async () => {
  const email = testEmail('member');
  const sub = await subscribe(email);
  await confirmPayment(sub.payment, CARDS.success);
  await waitFor(() => subscriptionOf(email), (r) => r.json?.status === 'active', { what: 'subscription to be active' });
  const again = await backend('POST', '/subscriptions', { body: { customer_email: email, plan_id: 'basic-monthly' } });
  assert.equal(again.status, 409, again.text);
});

test('L1 a declined payment leaves the subscription payment_failed', async () => {
  const email = testEmail('declined');
  const sub = await subscribe(email);
  await confirmPayment(sub.payment, CARDS.declined);
  const after = await waitFor(() => subscriptionOf(email), notPending, { what: 'subscription to settle' });
  assert.equal(after.json.status, 'payment_failed');
});

test('L1 POST /subscriptions rejects unknown plans (404) and bad requests (400)', async () => {
  const unknown = await backend('POST', '/subscriptions', { body: { customer_email: testEmail(), plan_id: 'no-such-plan' } });
  assert.equal(unknown.status, 404, unknown.text);
  const badEmail = await backend('POST', '/subscriptions', { body: { customer_email: 'nope', plan_id: 'basic-monthly' } });
  assert.equal(badEmail.status, 400, badEmail.text);
});

test('L2 a subscription webhook delivered twice still ends active', async () => {
  const email = testEmail('dup');
  const sub = await subscribe(email);
  await confirmPayment(sub.payment, CARDS.duplicateDelivery);
  await waitFor(() => subscriptionOf(email), notPending, { what: 'subscription to settle' });
  await sleep(2000);
  const after = await subscriptionOf(email);
  assert.equal(after.json.status, 'active');
  assert.equal(after.json.plan_id, 'basic-monthly');
});
