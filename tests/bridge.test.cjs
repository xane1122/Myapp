const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../MyAppBeta/NativeBridge.js'), 'utf8');

function context(origin = 'https://xanelove.com') {
  const posted = [];
  const listeners = {};
  const storage = new Map();
  const document = {
    readyState: 'loading',
    addEventListener(name, callback) { listeners[name] = callback; },
    querySelector() { return null; }
  };
  const location = new URL(origin + '/?conversation_id=47&message_id=902&beta_assistant=grok');
  const window = {
    location,
    webkit: { messageHandlers: { myappBeta: { postMessage(value) { posted.push(value); } } } },
    localStorage: { getItem(key) { return storage.get(key) ?? null; }, setItem(key, value) { storage.set(key, value); } },
    addEventListener() {}, dispatchEvent() {}, toast() {}
  };
  window.window = window; window.top = window;
  const sandbox = { window, location, document, localStorage: window.localStorage,
    URLSearchParams, URL, Math, Date, Promise, Map, Object, String,
    setTimeout, clearTimeout, setInterval() { return 1; }, clearInterval() {}, Event: class {}, CustomEvent: class {},
    HTMLInputElement: class {}, File: class {}, DataTransfer: class {} };
  return { sandbox, window, posted, listeners, storage };
}

test('bridge exists only on the exact production origin', () => {
  const good = context(); vm.runInNewContext(source, good.sandbox);
  assert.equal(good.window.MyAppNative.version, 1);
  assert.equal(good.storage.get('myassistant.active_assistant'), 'grok');
  const bad = context('https://xanelove.com.evil.test'); vm.runInNewContext(source, bad.sandbox);
  assert.equal(bad.window.MyAppNative, undefined);
});

test('native requests resolve once and carry method/payload', async () => {
  const value = context(); vm.runInNewContext(source, value.sandbox);
  const result = value.window.MyAppNative.request('ping', { hello: 'cat' });
  assert.equal(value.posted.length, 1);
  assert.equal(value.posted[0].method, 'ping');
  assert.equal(value.posted[0].payload.hello, 'cat');
  value.window.MyAppNative._resolve(value.posted[0].id, { version: 1 }, null);
  assert.equal((await result).version, 1);
  value.window.MyAppNative._resolve(value.posted[0].id, { version: 2 }, null);
});

test('page adapter reports committed assistant reply IDs', async () => {
  const value = context(); vm.runInNewContext(source, value.sandbox);
  value.window.readChatStream = async () => ({ conversation_id: 47, assistant_message_id: 902 });
  value.listeners.DOMContentLoaded();
  const reply = value.window.readChatStream();
  await reply;
  assert.equal(value.posted[0].method, 'reply.completed');
  assert.equal(value.posted[0].payload.conversation_id, '47');
  assert.equal(value.posted[0].payload.message_id, '902');
  value.window.MyAppNative._resolve(value.posted[0].id, { received: true }, null);
});
