/* MyApp Beta bridge v1. Injected by WKWebView into the main frame only.
 * This file is bundled in Beta; it is never deployed to the production website. */
(() => {
  'use strict';
  if (location.origin !== 'https://xanelove.com' || window !== window.top || window.MyAppNative) return;
  const pending = new Map();
  const pageID = Math.random().toString(36).slice(2) + Date.now().toString(36);
  let sequence = 0;
  const initialRoute = new URLSearchParams(location.search);
  const requestedAssistant = initialRoute.get('beta_assistant');
  if (requestedAssistant === 'rhys' || requestedAssistant === 'grok') {
    // Only the Beta sandbox's active UI channel. Never touch models or provider settings.
    try { localStorage.setItem('myassistant.active_assistant', requestedAssistant); } catch (_) {}
  }

  function request(method, payload = {}) {
    return new Promise((resolve, reject) => {
      if (pending.size >= 16) { reject(new Error('原生请求过多，请稍后重试。')); return; }
      const id = `${pageID}.${++sequence}`;
      const timeout = setTimeout(() => {
        pending.delete(id); reject(new Error('原生操作超时，请重试。'));
      }, method === 'media.pick' ? 300000 : 30000);
      pending.set(id, { resolve, reject, timeout });
      try { window.webkit.messageHandlers.myappBeta.postMessage({ id, method, payload }); }
      catch (error) { clearTimeout(timeout); pending.delete(id); reject(error); }
    });
  }

  const api = {
    version: 1,
    request,
    openSettings: () => request('settings.open'),
    pickMedia: options => request('media.pick', options),
    requestNotifications: () => request('notifications.request'),
    scheduleNotification: options => request('notifications.schedule', options),
    waitForReply: ticket => request('background.wait', ticket),
    _resolve(id, value, error) {
      const callback = pending.get(id);
      if (!callback) return;
      clearTimeout(callback.timeout); pending.delete(id);
      error ? callback.reject(new Error(error)) : callback.resolve(value);
    }
  };
  Object.defineProperty(window, 'MyAppNative', { value: Object.freeze(api), configurable: false, writable: false });

  const originalFetch = typeof window.fetch === 'function' ? window.fetch.bind(window) : null;
  function isNativeChatRequest(input, options) {
    try {
      const url = new URL(typeof input === 'string' || input instanceof URL ? input : input.url, location.href);
      const method = String(options?.method || input?.method || 'GET').toUpperCase();
      return url.origin === location.origin && url.pathname === '/api/chat' && method === 'POST';
    } catch (_) { return false; }
  }
  async function inputBody(input, options) {
    if (typeof options?.body === 'string') return options.body;
    if (input && typeof input.clone === 'function') return input.clone().text();
    throw new Error('原生后台回复只接受 JSON 请求。');
  }
  if (originalFetch && typeof Response === 'function') {
    window.fetch = async function (input, options = {}) {
      if (!isNativeChatRequest(input, options)) return originalFetch(input, options);
      const body = await inputBody(input, options);
      const idempotencyKey = window.crypto?.randomUUID?.();
      if (!idempotencyKey) throw new Error('当前系统无法创建后台回复任务。');
      const ticket = await request('reply.start', { body, idempotency_key: idempotencyKey });
      const resultResponse = await originalFetch(ticket.result_url, {
        method: 'GET', cache: 'no-store', headers: { 'X-MyApp-Reply-Token': ticket.result_token }, signal: options.signal
      });
      const result = await resultResponse.json().catch(() => ({}));
      if (!resultResponse.ok || result.status !== 'completed') throw new Error('后台回复未完成，请稍后刷新聊天记录。');
      const historyResponse = await originalFetch(`/api/history?conversation_id=${encodeURIComponent(result.conversation_id)}&limit=50`, { cache: 'no-store' });
      const historyData = await historyResponse.json().catch(() => ({}));
      const assistant = (historyData.history || []).find(item => String(item.id) === String(result.message_id) && item.role === 'assistant');
      if (!historyResponse.ok || !assistant) throw new Error('回复已保存，请刷新聊天记录查看。');
      const responseData = Object.assign({}, assistant, {
        conversation_id: result.conversation_id,
        assistant_message_id: result.message_id,
        user_message_id: result.user_message_id || 0,
        user_message_ids: result.user_message_ids || [],
        __myappNativeJob: true
      });
      return new Response(JSON.stringify(responseData), { status: 200, headers: { 'Content-Type': 'application/json' } });
    };
  }

  const messageID = value => /^[1-9]\d*$/.test(String(value)) && Number.isSafeInteger(Number(value));
  function reportCompletion(data, assistant) {
    if (!data || data.__myappNativeJob || !messageID(data.conversation_id) || !messageID(data.assistant_message_id)) return;
    request('reply.completed', {
      conversation_id: String(data.conversation_id), message_id: String(data.assistant_message_id), assistant
    }).catch(() => {});
  }

  function accepts(input, file) {
    const accepted = input.accept.toLowerCase().split(',').map(x => x.trim()).filter(Boolean);
    if (!accepted.length) return true;
    return accepted.some(x => x === '*/*' || (x.startsWith('.') ? file.name.toLowerCase().endsWith(x)
      : x.endsWith('/*') ? file.type.startsWith(x.slice(0, -1)) : file.type === x));
  }

  function nativeFile(item) {
    const bytes = Uint8Array.from(atob(item.base64), char => char.charCodeAt(0));
    return new File([bytes], item.name, { type: item.type, lastModified: Date.now() });
  }

  let choosing = false;
  document.addEventListener('click', async event => {
    const input = event.target;
    if (!(input instanceof HTMLInputElement) || input.type !== 'file' || input.disabled) return;
    // Images (including the existing mixed chat attachment input) use native pickers.
    // Other document-only inputs retain WebKit's standard system file selection.
    if (!/(image\/|\.(png|jpe?g|heic|heif|webp|gif))/i.test(input.accept)) return;
    event.preventDefault(); event.stopImmediatePropagation();
    if (choosing) return;
    choosing = true;
    try {
      const tokens = input.accept.toLowerCase().split(',').map(x => x.trim());
      const documents = tokens.some(x => !x.startsWith('image/') && !/^\.(png|jpe?g|heic|heif|webp|gif)$/.test(x));
      const selected = await api.pickMedia({ multiple: input.multiple, documents,
        source: input.hasAttribute('capture') ? 'camera' : 'choose' });
      if (!input.isConnected || !selected?.length) return;
      const files = selected.slice(0, input.multiple ? 5 : 1).map(nativeFile);
      // Normalized camera/photos are JPEG; accept image-only extension lists too.
      if (files.some(file => !accepts(input, file))) throw new Error('所选文件不符合此入口支持的类型。');
      const transfer = new DataTransfer(); files.forEach(file => transfer.items.add(file));
      input.files = transfer.files;
      input.dispatchEvent(new Event('input', { bubbles: true }));
      input.dispatchEvent(new Event('change', { bubbles: true }));
    } catch (error) {
      if (typeof window.toast === 'function') window.toast(error.message);
      else window.alert(error.message);
    } finally { choosing = false; }
  }, true);

  function installPageAdapters() {
    // Keep native controls in the existing settings menu, without permanent app chrome.
    const menu = document.querySelector('#settingsMenu .settings-menu-group');
    if (menu && !document.querySelector('#myapp-beta-settings')) {
      const button = document.createElement('button');
      button.id = 'myapp-beta-settings';
      button.type = 'button';
      button.textContent = 'MyApp Beta · 通知与应用设置';
      button.addEventListener('click', () => api.openSettings().catch(error => {
        if (typeof window.toast === 'function') window.toast(error.message);
      }));
      menu.appendChild(button);
    }
    // Existing production parser consumes NDJSON and returns the committed message ID.
    // Observe its return value; do not clone streams, retry POSTs, or copy request secrets.
    if (typeof window.readChatStream === 'function') {
      const original = window.readChatStream;
      window.readChatStream = async function (...args) {
        let assistant;
        try { assistant = localStorage.getItem('myassistant.active_assistant'); } catch (_) {}
        const result = await original.apply(this, args);
        reportCompletion(result, assistant);
        return result;
      };
    }
    window.dispatchEvent(new CustomEvent('myapp:native-ready', { detail: { version: 1 } }));
    const conversation = initialRoute.get('conversation_id');
    const message = initialRoute.get('message_id');
    if (!messageID(conversation) || !messageID(message)) return;
    let focusing;
    async function focusCapturedRoute() {
      if (focusing) return focusing;
      if (typeof window.jumpSearchResult !== 'function' || typeof window.switchTab !== 'function') return;
      focusing = (async () => {
        window.switchTab('chat', null, { deferScroll: true });
        await window.jumpSearchResult(Number(conversation), Number(message));
      })();
      return focusing;
    }
    // init() awaits history and then calls this global function. Use the captured IDs
    // even when loadConversations has already rewritten location.search.
    window.focusNotificationMessage = focusCapturedRoute;
    // If init() already completed before DOMContentLoaded (cached page), recover here.
    window.addEventListener('load', () => { focusCapturedRoute().catch(() => {}); }, { once: true });
    // The existing init() performs focusNotificationMessage() asynchronously.
    // Keep the native route pending until a matching message node is actually present.
    const started = Date.now();
    const timer = setInterval(() => {
      const node = document.querySelector(`[data-message-id="${message}"]`);
      if (node && node.classList.contains('message-focus')) {
        clearInterval(timer);
        window.switchTab('chat', null, { deferScroll: true });
        request('route.handled', { conversation_id: conversation, message_id: message }).catch(() => {});
      } else if (Date.now() - started > 30000) { clearInterval(timer); }
    }, 150);
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', installPageAdapters, { once: true });
  else installPageAdapters();
})();
