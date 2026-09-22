(() => {
  if (!window.WallpaperManager && !document.querySelector('script[data-wallpaper-loader]')) {
    const script = document.createElement('script');
    script.src = '/wallpaper.js?v=1';
    script.dataset.wallpaperLoader = 'true';
    document.head.appendChild(script);
  }
  const nativeFetch = window.fetch.bind(window);
  window.fetch = (input, init = {}) => {
    const url = typeof input === 'string' || input instanceof URL ? input : input.url;
    if (!new URL(url, window.location.href).pathname.startsWith('/api/')) {
      return nativeFetch(input, init);
    }
    const sourceHeaders = init.headers || (input instanceof Request ? input.headers : undefined);
    const headers = new Headers(sourceHeaders);
    if (window.APP_AUTH_TOKEN) headers.set('Authorization', `Bearer ${window.APP_AUTH_TOKEN}`);
    return nativeFetch(input, { ...init, headers, credentials: init.credentials || 'same-origin' });
  };

  const nativeSendBeacon = navigator.sendBeacon && navigator.sendBeacon.bind(navigator);
  if (nativeSendBeacon) {
    navigator.sendBeacon = (url, data) => {
      if (!new URL(url, window.location.href).pathname.startsWith('/api/')) {
        return nativeSendBeacon(url, data);
      }
      window.fetch(url, { method: 'POST', body: data, keepalive: true }).catch(() => {});
      return true;
    };
  }

  const nativeOpen = XMLHttpRequest.prototype.open;
  const nativeSend = XMLHttpRequest.prototype.send;
  XMLHttpRequest.prototype.open = function (method, url, ...rest) {
    this.__myappAPIRequest = new URL(url, window.location.href).pathname.startsWith('/api/');
    return nativeOpen.call(this, method, url, ...rest);
  };
  XMLHttpRequest.prototype.send = function (body) {
    if (this.__myappAPIRequest) {
      if (window.APP_AUTH_TOKEN) this.setRequestHeader('Authorization', `Bearer ${window.APP_AUTH_TOKEN}`);
    }
    return nativeSend.call(this, body);
  };
})();
