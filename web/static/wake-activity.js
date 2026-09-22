(function () {
  if (window.__myappWakeActivityStarted) return;
  window.__myappWakeActivityStarted = true;

  function loadPushRegistration() {
    if (window.MyAppPushRegistration || document.querySelector('script[data-myapp-push-registration]')) return;
    const script = document.createElement('script');
    script.src = '/push-registration.js?v=20260917-1';
    script.async = true;
    script.dataset.myappPushRegistration = 'true';
    (document.head || document.documentElement).appendChild(script);
  }

  let lastReportAt = 0;

  function conversationID() {
    const params = new URLSearchParams(location.search);
    const candidates = [
      params.get('conversation_id'),
      localStorage.getItem('myassistant.conversation_id'),
      localStorage.getItem('myassistant.conversation_id.grok'),
      '1',
    ];
    for (const value of candidates) {
      const id = Number(value);
      if (Number.isInteger(id) && id > 0) return id;
    }
    return 1;
  }

  function report(hidden, force) {
    const now = Date.now();
    if (!force && now - lastReportAt < 5000) return;
    lastReportAt = now;
    const source = (hidden ? 'page_hidden:' : 'page_visible:') + location.pathname;
    try {
      fetch('/api/wake/activity', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ conversation_id: conversationID(), source }),
        keepalive: true,
      }).catch(() => {});
    } catch (_) {}
  }

  function start() {
    loadPushRegistration();
    report(document.hidden, true);
    setInterval(() => {
      if (!document.hidden) report(false, false);
    }, 15000);
    document.addEventListener('visibilitychange', () => report(document.hidden, true));
    window.addEventListener('pageshow', () => report(false, true));
    window.addEventListener('pagehide', () => report(true, true));
  }

  window.WakeActivity = { report: () => report(document.hidden, true) };
  document.readyState === 'loading'
    ? document.addEventListener('DOMContentLoaded', start, { once: true })
    : start();
})();
