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
    crypto: { randomUUID() { return '12345678-1234-4123-8123-1234567890ab'; } },
    fetch: async () => { throw new Error('unexpected fetch'); },
    webkit: { messageHandlers: { myappBeta: { postMessage(value) { posted.push(value); } } } },
    localStorage: { getItem(key) { return storage.get(key) ?? null; }, setItem(key, value) { storage.set(key, value); } },
    addEventListener() {}, dispatchEvent() {}, toast() {}
  };
  window.window = window; window.top = window;
  const sandbox = { window, location, document, localStorage: window.localStorage,
    URLSearchParams, URL, Response, Math, Date, Promise, Map, Object, String,
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

test('settings menu opens native settings without adding controls to chat', async () => {
  const value = context();
  const children = [];
  const callbacks = {};
  value.sandbox.document.querySelector = selector => selector === '#settingsMenu .settings-menu-group'
    ? { appendChild(button) { children.push(button); } } : null;
  value.sandbox.document.createElement = () => ({ addEventListener(name, fn) { callbacks[name] = fn; } });
  vm.runInNewContext(source, value.sandbox);
  value.listeners.DOMContentLoaded();
  assert.equal(children.length, 1);
  assert.equal(children[0].id, 'myapp-beta-settings');
  callbacks.click();
  assert.equal(value.posted[0].method, 'settings.open');
  value.window.MyAppNative._resolve(value.posted[0].id, { opened: true }, null);
});

test('chat POST becomes a native background job and reconstructs the saved reply', async () => {
  const value = context();
  const calls = [];
  value.window.fetch = async (url, options) => {
    calls.push({ url: String(url), options });
    if (String(url).includes('/result')) return new Response(JSON.stringify({
      job_id: '12345678-1234-4123-8123-1234567890ab', status: 'completed', conversation_id: 47,
      message_id: 902, user_message_id: 901, user_message_ids: [901], assistant: 'grok'
    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
    return new Response(JSON.stringify({ history: [{ id: 902, conversation_id: 47, role: 'assistant', reply: 'done', content: 'done' }] }),
      { status: 200, headers: { 'Content-Type': 'application/json' } });
  };
  vm.runInNewContext(source, value.sandbox);
  const pending = value.window.fetch('/api/chat', { method: 'POST', body: JSON.stringify({ conversation_id: 47, message: 'hello' }) });
  await Promise.resolve();
  assert.equal(value.posted[0].method, 'reply.start');
  value.window.MyAppNative._resolve(value.posted[0].id, {
    job_id: '12345678-1234-4123-8123-1234567890ab', conversation_id: 47, assistant: 'grok',
    result_url: 'https://xanelove.com/api/native-replies/12345678-1234-4123-8123-1234567890ab/result', result_token: 'a'.repeat(43)
  }, null);
  const response = await pending, data = await response.json();
  assert.equal(data.assistant_message_id, 902);
  assert.equal(data.reply, 'done');
  assert.equal(data.__myappNativeJob, true);
  assert.equal(calls[0].options.headers['X-MyApp-Reply-Token'], 'a'.repeat(43));
});
