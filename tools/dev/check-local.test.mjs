import { test } from 'node:test';
import assert from 'node:assert/strict';
import { checkLocal } from './check-local.mjs';
const env = { DATABASE_URL:'postgres://local:fixture@127.0.0.1:5432/docmatch', DM_API_BASE_URL:'http://localhost:8080', DM_PUBLIC_BASE_URL:'http://localhost:4321', DM_API_LISTEN:'127.0.0.1:8080', DM_ADMIN_LISTEN:'127.0.0.1:8081' };
test('local configuration is accepted; copied production credentials and public listeners are refused', () => {
  assert.doesNotThrow(() => checkLocal(env));
  for (const [name,value] of Object.entries({ DATABASE_URL:'postgres://local:fixture@database.example.invalid/db', DM_API_BASE_URL:'https://api.example.invalid', DM_API_LISTEN:'0.0.0.0:8080', DM_ADMIN_LISTEN:':8081' }))
    assert.throws(() => checkLocal({ ...env, [name]:value }),new RegExp(name));
});
