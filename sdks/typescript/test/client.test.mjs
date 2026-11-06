import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHmac } from 'node:crypto';
import { Client, verifyWebhook, DocmatchError } from '../dist/esm/index.js';
test('retry uses stable idempotency; upload writes are not retried', async () => {
  let keys = [],
    attempts = 0;
  const client = new Client({
    baseURL: 'https://api.example.invalid',
    apiKey: 'fixture',
    fetch: async (_url, opts) => {
      attempts++;
      keys.push(opts.headers['Idempotency-Key']);
      return attempts === 1
        ? new Response('{}', { status: 429, headers: { 'Retry-After': '0' } })
        : Response.json({ id: 'run_fixture', status: 'queued' });
    },
  });
  await client.run({ id: 'q', signature: 's' }, client.input(['upl_fixture']));
  assert.equal(attempts, 2);
  assert.equal(keys[0], keys[1]);
  assert.ok(keys[0]);
  attempts = 0;
  const unsafe = new Client({
    baseURL: 'https://api.example.invalid',
    apiKey: 'fixture',
    fetch: async () => {
      attempts++;
      return new Response('{}', { status: 503 });
    },
  });
  await assert.rejects(unsafe.request('POST', '/v1/uploads', {}), DocmatchError);
  assert.equal(attempts, 1);
});
test('storage request has no API auth or cookies', async () => {
  let storage = false;
  const client = new Client({
    baseURL: 'https://api.example.invalid',
    apiKey: 'fixture-secret',
    fetch: async (url, opts) => {
      if (url === 'https://storage.example.invalid/file') {
        storage = true;
        assert.equal(opts.headers, undefined);
        assert.equal(opts.credentials, 'omit');
        assert.equal(opts.redirect, 'error');
        assert.ok(opts.body instanceof FormData);
        return new Response(null, { status: 204 });
      }
      return Response.json(
        url.endsWith('/complete')
          ? { id: 'upl_fixture', status: 'ready' }
          : {
              id: 'upl_fixture',
              method: 'POST',
              url: 'https://storage.example.invalid/file',
              fields: { policy: 'fixture' },
            },
      );
    },
  });
  await client.upload('a.csv', new TextEncoder().encode('a,b\n1,2'), 'text/csv');
  assert.ok(storage);
});
test('signature rejects tampering, expiry and duplicate timestamps', async () => {
  const body = new TextEncoder().encode('{}'),
    timestamp = '1234567890',
    secret = 'fixture';
  const signature = createHmac('sha256', secret)
    .update(timestamp + '.')
    .update(body)
    .digest('hex');
  const header = `t=${timestamp},v1=${signature}`;
  assert.ok(await verifyWebhook(body, header, secret, Number(timestamp) * 1000));
  assert.equal(
    await verifyWebhook(new Uint8Array([1]), header, secret, Number(timestamp) * 1000),
    false,
  );
  assert.equal(await verifyWebhook(body, header, secret, (Number(timestamp) + 301) * 1000), false);
  assert.equal(
    await verifyWebhook(body, header + ',t=' + timestamp, secret, Number(timestamp) * 1000),
    false,
  );
});
