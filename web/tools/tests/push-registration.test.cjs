const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const source = fs.readFileSync('static/push-registration.js', 'utf8');

async function flush() {
  await new Promise(resolve => setImmediate(resolve));
  await new Promise(resolve => setImmediate(resolve));
}

async function grantedPermissionRegistersSubscription() {
  const requests = [];
  const subscription = {
    endpoint: 'https://push.example/device',
    toJSON: () => ({ keys: { p256dh: 'p', auth: 'a' } }),
  };
  const registration = {
    update: async () => {},
    pushManager: {
      getSubscription: async () => subscription,
      subscribe: async () => { throw new Error('existing subscription should be reused'); },
    },
  };
  const storage = new Map();
  const context = {
    window: null,
    navigator: {
      userAgent: 'Desktop', platform: 'Win32', maxTouchPoints: 0,
      serviceWorker: { register: async () => registration },
    },
    Notification: { permission: 'granted', requestPermission: async () => 'granted' },
    PushManager: function PushManager() {},
    localStorage: { getItem: key => storage.get(key) || null, setItem: (key, value) => storage.set(key, value) },
    crypto: { randomUUID: () => 'device-user' },
    fetch: async (url, options = {}) => {
      requests.push({ url, options });
      return url === '/api/push' && !options.method
        ? { ok: true, json: async () => ({ public_key: 'AQID' }) }
        : { ok: true };
    },
    atob: value => Buffer.from(value, 'base64').toString('binary'),
    Uint8Array, setTimeout, clearTimeout, console,
    addEventListener() {}, removeEventListener() {}, isSecureContext: true,
  };
  context.window = context;
  vm.runInNewContext(source, context);
  await flush();
  const post = requests.find(request => request.options.method === 'POST');
  assert.ok(post, 'subscription POST was not sent');
  const body = JSON.parse(post.options.body);
  assert.equal(body.user_id, 'device-user');
  assert.equal(body.endpoint, subscription.endpoint);
  assert.deepEqual(body.keys, { p256dh: 'p', auth: 'a' });
}

async function iosWaitsForGesture() {
  const listeners = new Map();
  let permissionRequests = 0;
  const context = {
    window: null,
    navigator: { userAgent: 'iPhone', platform: 'iPhone', maxTouchPoints: 5, serviceWorker: {} },
    Notification: { permission: 'default', requestPermission: async () => { permissionRequests++; return 'denied'; } },
    PushManager: function PushManager() {}, localStorage: {}, crypto: {}, fetch: async () => ({ ok: true }),
    atob: value => value, Uint8Array, console, isSecureContext: true,
    addEventListener: (name, handler) => listeners.set(name, handler),
    removeEventListener: name => listeners.delete(name),
  };
  context.window = context;
  vm.runInNewContext(source, context);
  await flush();
  assert.equal(permissionRequests, 0, 'iOS permission request must wait for a gesture');
  assert.ok(listeners.has('pointerdown'), 'gesture fallback was not installed');
  await listeners.get('pointerdown')();
  assert.equal(permissionRequests, 1);
}

(async () => {
  await grantedPermissionRegistersSubscription();
  await iosWaitsForGesture();
  console.log('push registration tests passed');
})().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
