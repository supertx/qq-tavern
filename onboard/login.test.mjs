import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm, mkdir, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { credential, login, LoginError } from './login.mjs';

const first = {appId: 'test-bot', appSecret: 'synthetic-secret'};
async function fixture(t) {
  const dir = await mkdtemp(join(tmpdir(), 'qq-tavern-login-'));
  t.after(() => rm(dir, {recursive: true, force: true}));
  const logs = [];
  return {privateDir: join(dir, 'private'), publicDir: join(dir, 'public'),
    connect: async () => first, log: text => logs.push(text), logs};
}

test('SDK object and single-element array credentials; ambiguous and malformed data rejected', () => {
  const expected = {app_id: first.appId, app_secret: first.appSecret};
  assert.deepEqual(credential(first), expected);
  assert.deepEqual(credential([first]), expected);
  assert.deepEqual(credential(expected), expected);
  for (const value of [null, [], [first, first], {}, {...first, appId: '../other'}, {...first, appSecret: '\n'}, {...first, appSecret: 'x'.repeat(4097)}]) {
    assert.throws(() => credential(value), LoginError);
  }
});

test('successful scan persists private credentials and public AppID, cached restart repairs AppID without scanning', async t => {
  const f = await fixture(t);
  assert.equal(await login(f), first.appId);
  const secretFile = join(f.privateDir, 'credentials.json');
  const idFile = join(f.publicDir, 'app-id');
  assert.deepEqual(JSON.parse(await readFile(secretFile, 'utf8')), credential(first));
  assert.equal((await stat(secretFile)).mode & 0o777, 0o600);
  assert.equal((await stat(f.privateDir)).mode & 0o777, 0o700);
  assert.equal(await readFile(idFile, 'utf8'), first.appId + '\n');
  assert.ok(!f.logs.join().includes(first.appSecret));
  await rm(idFile);
  assert.equal(await login({...f, connect: () => assert.fail('cached login must not scan')}), first.appId);
  assert.equal(await readFile(idFile, 'utf8'), first.appId + '\n');
});

test('rebind replaces both records; failed, invalid, and cancelled rebind preserve previous binding', async t => {
  const f = await fixture(t);
  await login(f);
  const before = await readFile(join(f.privateDir, 'credentials.json'), 'utf8');
  const controller = new AbortController();
  for (const connect of [
    async () => {throw new Error('provider-error-containing-' + first.appSecret);},
    async () => [first, first],
    async () => {controller.abort(); return {appId: 'new', appSecret: 'new-secret'};},
  ]) {
    await assert.rejects(login({...f, force: true, connect, signal: controller.signal}), e => e instanceof LoginError && !e.message.includes(first.appSecret));
    assert.equal(await readFile(join(f.privateDir, 'credentials.json'), 'utf8'), before);
    assert.equal(await readFile(join(f.publicDir, 'app-id'), 'utf8'), first.appId + '\n');
  }
  await login({...f, force: true, connect: async () => ({appId: 'new', appSecret: 'new-secret'})});
  assert.equal(await readFile(join(f.publicDir, 'app-id'), 'utf8'), 'new\n');
  assert.equal(JSON.parse(await readFile(join(f.privateDir, 'credentials.json'), 'utf8')).app_id, 'new');
});

test('corrupt binding requires explicit rebind and never silently scans', async t => {
  const f = await fixture(t);
  await mkdir(f.privateDir);
  await writeFile(join(f.privateDir, 'credentials.json'), '{broken');
  await assert.rejects(login({...f, connect: () => assert.fail('unexpected scan')}), /--rebind/);
  await login({...f, force: true});
});

test('concurrent scan is rejected and lock released after completion', async t => {
  const f = await fixture(t);
  let finish, started;
  const ready = new Promise(resolve => {started = resolve;});
  const running = login({...f, connect: () => {started(); return new Promise(resolve => {finish = resolve;});}});
  await ready;
  await assert.rejects(login(f), /已有扫码任务/);
  finish(first);
  await running;
  await login(f);
});

test('public write failure does not announce success, next run repairs without another scan', async t => {
  const f = await fixture(t);
  await mkdir(f.publicDir, {recursive: true});
  await mkdir(join(f.publicDir, 'app-id'));
  await assert.rejects(login(f));
  assert.ok(!f.logs.some(x => x.includes('已保存')));
  await rm(join(f.publicDir, 'app-id'), {recursive: true});
  assert.equal(await login({...f, connect: () => assert.fail('must reuse persisted private binding')}), first.appId);
});
