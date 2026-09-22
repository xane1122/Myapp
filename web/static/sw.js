const CACHE = 'myapp-v969-reply-failure';
const STATIC = [
  '/', '/home', '/index.html', '/settings.html', '/context.html', '/diary.html', '/activity.html', '/world-book.html',
  '/catroom', '/catroom.html', '/roleplay/', '/training/',
  '/styles.css?v=20260914-keyboard-overlay', '/native-feel.js?v=20260917-explicit-viewport', '/wallpaper.js?v=20260914-fixed-chat-wallpaper', '/image-normalize.js?v=20260911-1',
  '/feature-page-base.css', '/feature-page-base.js', '/feature-placeholder.js',
  '/bubble-skins.css?v=20260912-3', '/bubble-skins.js?v=20260914-settings-entry', '/bubble-skins/bubbles.json?v=1', '/bubble-skins/default-cat.svg?v=2', '/wake-activity.js?v=20260911-1', '/world-book.css?v=20260909-world-book-v2',
  '/push-registration.js?v=20260918-chat-state',
  '/catroom/cat-pet.css?v=5', '/catroom/cat-pet.js?v=1',
  '/catroom/catroom.css?v=9', '/catroom/catroom.js?v=15',
  '/catroom/xiaoqi-idle.png?v=2', '/catroom/xiaoqi-sleep.png?v=2',
  '/catroom/xiaoqi-walk.png?v=2', '/catroom/xiaoqi-wait.png?v=2',
  '/catroom/xiaoqi-play.png?v=2', '/catroom/xiaoqi-sulk.png?v=2',
  '/catroom/icon-192.png', '/catroom/icon-512.png', '/catroom/manifest.json?v=1',
  '/training/page.js?v=20260723-2', '/training/training.css?v=20260723-2',
  '/training/training-sheet.css?v=20260723',
  '/home-background.png?v=101', '/chat-background.jpg?v=48', '/bell-wallpaper.png?v=1',
  '/feature-icons/closet.jpg?v=121', '/feature-icons/cat-nest.jpg?v=121',
  '/feature-icons/bell.jpg?v=121', '/feature-icons/album.jpg?v=121',
  '/feature-icons/game.jpg?v=121', '/feature-icons/ledger.jpg?v=121',
  '/feature-icons/notes.jpg?v=121', '/feature-icons/roleplay.jpg?v=121',
  '/feature-icons/training.jpg?v=121', '/feature-icons/daily-log.jpg?v=121',
  '/feature-icons/notebook.jpg?v=121', '/vendor/marked.umd.js?v=18.0.5',
  '/vendor/purify.min.js?v=3.4.11', '/manifest.json', '/icon-192.png', '/icon-512.png'
];

self.addEventListener('install', e => {
  e.waitUntil(
    caches.open(CACHE).then(c => c.addAll(STATIC)).then(() => self.skipWaiting())
  );
});

self.addEventListener('activate', e => {
  e.waitUntil(
    caches.keys().then(keys =>
      Promise.all(keys.filter(k => k !== CACHE).map(k => caches.delete(k)))
    ).then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', e => {
  const url = new URL(e.request.url);

  // Let API, cross-origin and non-GET requests use the browser network stack.
  // A rejected fetch passed to respondWith is reported by Safari as a service
  // worker failure and can make background wake polling look broken.
  if (url.origin !== self.location.origin || e.request.method !== 'GET' || url.pathname.startsWith('/api/')) return;

  if (url.pathname.startsWith('/uploads/stickers/')) {
    e.respondWith(
      caches.match(e.request).then(cached => {
        if (cached) return cached;
        return fetch(e.request).then(resp => {
          const copy = resp.clone();
          caches.open(CACHE).then(c => c.put(e.request, copy));
          return resp;
        });
      })
    );
    return;
  }

  // 页面和静态资源：网络优先，避免部署后继续显示旧设置面板。
  e.respondWith(
    fetch(e.request)
      .then(resp => {
        if (resp.ok) {
          const copy = resp.clone();
          caches.open(CACHE).then(c => c.put(e.request, copy)).catch(() => {});
        }
        return resp;
      })
      .catch(async () => {
        const cached = await caches.match(e.request);
        if (cached) return cached;
        if (e.request.mode === 'navigate') {
          const shell = await caches.match('/index.html');
          if (shell) return shell;
        }
        return new Response('Offline', { status: 503, headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
      })
  );
});

self.addEventListener('push', event => {
  let data = {};
  try { data = event.data ? event.data.json() : {}; } catch { data = { body: event.data ? event.data.text() : '' }; }
  const options = {
    body: data.body || `${data.title || '助手'} 给你发了一条消息`,
    icon: '/icon-192.png',
    badge: '/icon-192.png',
    // A fixed tag lets iOS coalesce later wake messages into an existing,
    // already-dismissed notification. Use the message id so every proactive
    // message is presented as a new notification.
    tag: data.message_id ? `rhys-${data.source || 'message'}-${data.message_id}` : `rhys-${data.source || 'message'}-${Date.now()}`,
    renotify: true,
    data: { url: data.url || '/', source: data.source || 'unknown', conversation_id: data.conversation_id, message_id: data.message_id }
  };
  event.waitUntil(self.registration.showNotification(data.title || '助手', options));
});

self.addEventListener('notificationclick', event => {
  event.notification.close();
  const target = new URL(event.notification.data?.url || '/', self.location.origin).href;
  event.waitUntil(clients.matchAll({ type: 'window', includeUncontrolled: true }).then(list => {
    for (const client of list) {
      if ('focus' in client) { client.navigate(target); return client.focus(); }
    }
    return clients.openWindow ? clients.openWindow(target) : undefined;
  }));
});
