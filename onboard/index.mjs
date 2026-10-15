import { startQrConnect } from '@tencent-connect/qqbot-connector';
import QRCode from 'qrcode-terminal';
import { login, LoginError } from './login.mjs';

const controller = new AbortController();
const timer = setTimeout(() => controller.abort(), 5 * 60 * 1000);
for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => controller.abort());

function connect(signal) {
  return new Promise((resolve, reject) => {
    startQrConnect({
      onQrDisplayed(url) { QRCode.generate(url, {small: true}); },
      onQrExpired() { console.log('二维码已过期，正在刷新。'); },
      onSuccess: resolve,
      onFailure: reject,
    }, {source: 'qq-tavern', signal, silent: true, log: () => {}, maxRefreshCount: 3});
  });
}

try {
  await login({privateDir: '/binding/private', publicDir: '/binding/public', connect,
    signal: controller.signal, force: process.env.QQ_LOGIN_FORCE === 'true'});
} catch (error) {
  console.error(error instanceof LoginError ? error.message : '无法保存 QQ 绑定，请检查数据卷权限和剩余空间。');
  process.exitCode = controller.signal.aborted ? 130 : 1;
} finally {
  clearTimeout(timer);
}
