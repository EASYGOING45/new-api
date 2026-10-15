import { test } from 'node:test';
import assert from 'node:assert/strict';
import { protectTransport } from './transport-policy.mjs';

test('429 respects Retry-After, permits one recovery probe and closes after success', async () => {
  let time = 0, calls = 0, resolve;
  const p = protectTransport({ fetch: async () => {
    calls++;
    if (calls === 1) return new Response('', { status: 429, headers: { 'retry-after': '600' } });
    return new Promise(r => { resolve = r; });
  } }, { now: () => time });
  assert.equal((await p.transport.fetch('test')).status, 429);
  assert.equal(p.snapshot().retry_after_s, 600);
  assert.equal((await p.transport.fetch('test')).status, 503);
  assert.equal(calls, 1);
  time = 600_001;
  const recovery = p.transport.fetch('test');
  assert.equal((await p.transport.fetch('test')).status, 503);
  assert.equal(p.snapshot().state, 'half_open');
  resolve(new Response('ok'));
  assert.equal(await (await recovery).text(), 'ok');
  assert.equal(p.snapshot().state, 'closed');
});

test('network errors open a circuit, caller abort and request 4xx do not', async () => {
  const p = protectTransport({ fetch: async () => { throw new Error('SECRET must never escape'); } });
  for (let i = 0; i < 2; i++) await assert.rejects(p.transport.fetch('test'), /account_transport_failed/);
  assert.equal(p.snapshot().state, 'open');
  assert.equal(p.snapshot().in_flight, 0);
  const q = protectTransport({ fetch: async () => new Response('', { status: 400 }) });
  await q.transport.fetch('test');
  assert.equal(q.snapshot().consecutive_failures, 0);
  const controller = new AbortController(); controller.abort();
  await assert.rejects(q.transport.fetch('test', { signal: controller.signal }));
  assert.equal(q.snapshot().state, 'closed');
});

test('invalid account credentials remain disabled', async () => {
  let calls = 0;
  const p = protectTransport({ fetch: async () => { calls++; return new Response('', { status: 401 }); } });
  await p.transport.fetch('test');
  assert.equal((await p.transport.fetch('test')).status, 401);
  assert.equal(calls, 1);
  assert.equal(p.snapshot().state, 'disabled');
});

test('concurrency is held until the response is consumed', async () => {
  const p = protectTransport({ fetch: async () => new Response('ok') }, { maxConcurrency: 1 });
  const first = await p.transport.fetch('test');
  assert.equal((await p.transport.fetch('test')).status, 429);
  assert.equal(p.snapshot().consecutive_failures, 0);
  await first.text();
  assert.equal(p.snapshot().in_flight, 0);
});

test('truncated and explicit-error streams count as failures; DONE completes', async () => {
  for (const [body, fails] of [
    ['data: {"choices":[]}\n\n', true],
    ['data: {"error":{"code":"fixture"}}\n\ndata: [DONE]\n\n', true],
    ['data: {"choices":[]}\n\ndata: [DONE]\n\n', false],
  ]) {
    const p = protectTransport({ fetch: async () => new Response(body, { headers: { 'content-type': 'text/event-stream' } }) }, { failureThreshold: 1 });
    const response = await p.transport.fetch('test');
    if (!body.includes('[DONE]')) await assert.rejects(response.text(), /account_transport_failed/);
    else await response.text();
    assert.equal(p.snapshot().state, fails ? 'open' : 'closed');
  }
});

test('first-event timeout cancels a heartbeat-only stream and releases the slot', async () => {
  let cancelled = false;
  const body = new ReadableStream({ start(c) { c.enqueue(new TextEncoder().encode(': heartbeat\n\n')); }, cancel() { cancelled = true; } });
  const p = protectTransport({ fetch: async () => new Response(body, { headers: { 'content-type': 'text/event-stream' } }) }, { firstEventMs: 10, failureThreshold: 1 });
  const response = await p.transport.fetch('test');
  await assert.rejects(response.text(), e => e.status === 504);
  assert.equal(cancelled, true);
  assert.equal(p.snapshot().in_flight, 0);
  assert.equal(p.snapshot().state, 'open');
});

test('client cancellation closes upstream without degrading the account', async () => {
  let cancelled = false;
  const p = protectTransport({ fetch: async () => new Response(new ReadableStream({ cancel() { cancelled = true; } })) });
  const response = await p.transport.fetch('test');
  await response.body.cancel();
  assert.equal(cancelled, true);
  assert.equal(p.snapshot().in_flight, 0);
  assert.equal(p.snapshot().state, 'closed');
});
