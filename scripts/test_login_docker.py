#!/usr/bin/env python3
"""Smoke test real image UIDs, volume handoff and both Compose configurations.
Build images first. All credentials are synthetic; containers have no network.
"""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import uuid

root = Path(__file__).resolve().parents[1]
version = (root / 'VERSION').read_text().strip()
app = os.getenv('TEST_APP_IMAGE', 'qq-tavern:' + version)
helper = os.getenv('TEST_LOGIN_IMAGE', 'qq-tavern-login:' + version)
prefix = 'qq-tavern-login-test-' + uuid.uuid4().hex[:12]
private, public = prefix + '-private', prefix + '-public'

def docker(*args):
    return subprocess.check_output(['docker', *args], cwd=root, text=True, stderr=subprocess.STDOUT)

with tempfile.TemporaryDirectory() as directory:
    env = Path(directory) / 'test.env'
    env.write_text('INTERNAL_API_TOKEN=' + 'x' * 32 + '\nMODEL_BASE_URL=https://model.example/v1\nMODEL_API_KEY=test-key\nMODEL_NAME=test-model\nQQ_APP_ID=manual-bot\nQQ_APP_SECRET=synthetic-secret\n')
    compose = ['compose', '--env-file', str(env), '-p', prefix, '-f', 'compose.yaml']
    manual = json.loads(docker(*compose, 'config', '--format', 'json'))['services']
    qr = json.loads(docker(*compose, '-f', 'compose.qr.yaml', '--profile', 'setup', 'config', '--format', 'json'))['services']
    assert manual['gateway']['environment']['QQ_APP_ID'] == 'manual-bot'
    assert 'QQ_APP_SECRET' not in manual['worker']['environment']
    assert qr['gateway']['environment']['QQ_APP_ID'] == ''
    assert qr['gateway']['environment']['QQ_APP_SECRET'] == ''
    assert qr['worker']['environment']['QQ_APP_ID'] == ''
    assert 'QQ_APP_SECRET' not in qr['worker']['environment']
    assert 'QQ_CREDENTIALS_FILE' not in qr['worker']['environment']
    assert not any(v['source'] == 'qq-private' for v in qr['worker']['volumes'])
    assert any(v['source'] == 'qq-public' and v['read_only'] for v in qr['worker']['volumes'])
    assert qr['qq-login']['logging']['driver'] == 'none'
    assert 'setup' in qr['qq-login']['profiles']

try:
    for volume in (private, public):
        docker('volume', 'create', volume)
    mounts = ['-v', private + ':/binding/private', '-v', public + ':/binding/public']
    # Import the actual login implementation but inject a successful SDK result.
    code = '''import {login} from './login.mjs';
import {stat,readFile} from 'node:fs/promises';
import assert from 'node:assert/strict';
assert.equal(process.getuid(),10001);
const options={privateDir:'/binding/private',publicDir:'/binding/public',log:()=>{},connect:async()=>({appId:'smoke-bot',appSecret:'synthetic-secret'})};
await login(options);
assert.equal((await stat('/binding/private/credentials.json')).mode&511,384);
assert.equal(await readFile('/binding/public/app-id','utf8'),'smoke-bot\\n');
await login({...options,connect:()=>{throw Error('cached binding must not scan')}});
console.log('binding persistence and cached restart passed');'''
    print(docker('run', '--rm', '--network', 'none', '--read-only', *mounts, '--entrypoint', 'node', helper, '--input-type=module', '-e', code).strip())
    common = ['-e', 'INTERNAL_API_TOKEN=' + 'x' * 32]
    model = ['-e', 'MODEL_BASE_URL=https://model.example/v1', '-e', 'MODEL_API_KEY=test-key', '-e', 'MODEL_NAME=test-model']
    cases = [
        ['-e', 'ROLE=gateway', '-e', 'QQ_APP_ID=manual-bot', '-e', 'QQ_APP_SECRET=synthetic-secret'],
        ['-e', 'ROLE=worker', '-e', 'QQ_APP_ID=manual-bot', *model],
        ['-e', 'ROLE=gateway', '-e', 'QQ_CREDENTIALS_FILE=/binding/private/credentials.json', '-v', private + ':/binding/private:ro'],
        ['-e', 'ROLE=worker', '-e', 'QQ_APP_ID_FILE=/binding/public/app-id', '-v', public + ':/binding/public:ro', *model],
    ]
    for case in cases:
        print(docker('run', '--rm', '--network', 'none', '--read-only', *common, *case, app, 'check').strip())
finally:
    for volume in (private, public):
        docker('volume', 'rm', volume)
print('Manual and QR Compose/containers passed; synthetic credentials only.')
