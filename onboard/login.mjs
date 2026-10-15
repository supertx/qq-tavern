import { randomUUID } from 'node:crypto';
import { mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

export class LoginError extends Error {}

export function credential(value) {
  const candidates = Array.isArray(value) ? value : [value];
  if (candidates.length !== 1) throw new LoginError('扫码未返回唯一机器人，请使用手动方式指定机器人。');
  const item = candidates[0];
  const appId = item?.appId ?? item?.app_id;
  const appSecret = item?.appSecret ?? item?.app_secret;
  if (typeof appId !== 'string' || !/^[A-Za-z0-9_-]{1,64}$/u.test(appId) ||
      typeof appSecret !== 'string' || !appSecret.trim() || appSecret.length > 4096 || /[\x00-\x1f\x7f]/u.test(appSecret)) {
    throw new LoginError('扫码返回的机器人凭据不完整，请重新扫码。');
  }
  return {app_id: appId, app_secret: appSecret};
}

async function atomicFile(path, contents) {
  const temporary = `${path}.${randomUUID()}.tmp`;
  try {
    await writeFile(temporary, contents, {mode: 0o600, flag: 'wx'});
    await rename(temporary, path);
  } finally {
    await rm(temporary, {force: true});
  }
}

export async function login({privateDir, publicDir, connect, signal, force = false, log = console.log}) {
  await mkdir(privateDir, {recursive: true, mode: 0o700});
  await mkdir(publicDir, {recursive: true, mode: 0o700});
  // A second login must not race the first one and publish a different AppID.
  const lock = join(privateDir, '.login-lock');
  try { await mkdir(lock, {mode: 0o700}); }
  catch (error) {
    if (error.code === 'EEXIST') throw new LoginError('已有扫码任务运行。若上次被强制终止，请按 README 清理登录锁。');
    throw error;
  }
  try {
    const destination = join(privateDir, 'credentials.json');
    if (!force) {
      let saved;
      try { saved = credential(JSON.parse(await readFile(destination, 'utf8'))); }
      catch (error) {
        if (error.code !== 'ENOENT') throw new LoginError('已保存的 QQ 绑定无法读取，请使用 qr --rebind 重新扫码。');
      }
      if (saved) {
        await atomicFile(join(publicDir, 'app-id'), `${saved.app_id}\n`);
        log('已使用保存的 QQ 绑定。');
        return saved.app_id;
      }
    }
    if (signal?.aborted) throw new LoginError('扫码已取消，原绑定保持不变。');
    log('请使用手机 QQ 扫描下方二维码，并在 QQ 内确认机器人授权。');
    let saved;
    try { saved = credential(await connect(signal)); }
    catch (error) {
      if (signal?.aborted) throw new LoginError('扫码已取消或超时，原绑定保持不变。');
      if (error instanceof LoginError) throw error;
      throw new LoginError('扫码连接失败，请检查网络后重试。');
    }
    if (signal?.aborted) throw new LoginError('扫码已取消，原绑定保持不变。');
    // Consumers start only after both writes succeed. A later run repairs AppID
    // from the private record if the process exits between the two writes.
    await atomicFile(destination, JSON.stringify(saved) + '\n');
    await atomicFile(join(publicDir, 'app-id'), `${saved.app_id}\n`);
    log(`QQ 绑定已保存（AppID: ${saved.app_id}）。`);
    return saved.app_id;
  } finally {
    await rm(lock, {recursive: true, force: true});
  }
}
