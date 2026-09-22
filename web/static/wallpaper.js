(function () {
  const STORAGE_KEY = 'myassistant.wallpaper';
  const CONFIG_KEY = 'myassistant.wallpaper.config';
  const PAGE_PREFIX = 'myassistant.wallpaper.page.';
  const MAX_EDGE = 1400;
  const MAX_BYTES = 280 * 1024;
  const TARGETS = ['all', 'home', 'chat', 'context', 'catroom', 'bell', 'album', 'games', 'ledger', 'notes', 'roleplay', 'training', 'daily-log', 'notebook', 'days-matter', 'mailbox', 'diary', 'settings'];
  let syncRevision = 0;
  let syncEnabled = false;
  let syncError = '';

  const style = document.createElement('style');
  style.textContent = `
    html.custom-wallpaper { background: var(--bg, #f8f5f4) !important; }
    html.custom-wallpaper body,
    html.custom-wallpaper body::before,
    html.custom-wallpaper body.home-bg::before,
    html.custom-wallpaper body.chat-bg::before,
    html.custom-wallpaper #app.feature-standalone-app,
    html.custom-wallpaper .workbench,
    html.custom-wallpaper .training-app,
    html.custom-wallpaper .ludo-shell { background-color: transparent !important; background-image: none !important; }
    .custom-wallpaper-layer { position: fixed; z-index: -2; top: -5vh; top: -5lvh; right: -5vw; bottom: auto; left: -5vw; width: 110vw; height: 110vh; height: 110lvh; pointer-events: none; background-image: var(--custom-wallpaper-image); background-position: var(--custom-wallpaper-x, 50%) var(--custom-wallpaper-y, 50%); background-size: cover; background-repeat: no-repeat; transform: scale(var(--custom-wallpaper-zoom, 1)); transform-origin: center; }
    html.custom-icon-color button svg, html.custom-icon-color .tab-item svg, html.custom-icon-color .icon-btn { color: var(--custom-icon-color) !important; stroke: var(--custom-icon-color) !important; }
  `;
  document.head.appendChild(style);

  function pageKey() {
    const path = location.pathname.replace(/^\/+|\/+$/g, '') || 'home';
    if (path === 'home' || path === 'index.html') return document.body?.classList.contains('chat-bg') ? 'chat' : 'home';
    if (!path && document.body?.classList.contains('chat-bg')) return 'chat';
    if (path === 'game' || path.startsWith('games')) return 'games';
    return path.replace(/\/index\.html$/, '').replace(/\.html$/, '').split('/')[0] || 'home';
  }
  function imageKey(target) { return target === 'all' ? STORAGE_KEY : `${PAGE_PREFIX}${target}.image`; }
  function configKey(target) { return target === 'all' ? CONFIG_KEY : `${PAGE_PREFIX}${target}.config`; }
  function ownImage(target) { return localStorage.getItem(imageKey(target)) || ''; }
  function get(target = pageKey()) { return ownImage(target) || (target === 'all' ? '' : ownImage('all')); }
  function getConfig(target = pageKey()) {
    const fallback = target === 'all' ? {} : getConfig('all');
    try { return { x: 50, y: 50, zoom: 1, iconColor: '', ...fallback, ...JSON.parse(localStorage.getItem(configKey(target)) || '{}') }; }
    catch { return { x: 50, y: 50, zoom: 1, iconColor: '' }; }
  }
  function ensureLayer() {
    if (!document.body) return document.addEventListener('DOMContentLoaded', ensureLayer, { once: true });
    let layer = document.querySelector('.custom-wallpaper-layer');
    if (!layer) { layer = document.createElement('div'); layer.className = 'custom-wallpaper-layer'; document.body.prepend(layer); }
    return layer;
  }
  function apply(value = get(), config = getConfig()) {
    const root = document.documentElement;
    const iconColor = /^#[0-9a-f]{6}$/i.test(config.iconColor || '') ? config.iconColor : '';
    root.classList.toggle('custom-icon-color', Boolean(iconColor));
    if (iconColor) {
      root.style.setProperty('--custom-icon-color', iconColor);
      root.style.setProperty('--accent', iconColor);
      root.style.setProperty('--accent2', iconColor);
    } else {
      root.style.removeProperty('--custom-icon-color');
      root.style.removeProperty('--accent');
      root.style.removeProperty('--accent2');
    }
    if (!value) {
      root.classList.remove('custom-wallpaper');
      root.style.removeProperty('--custom-wallpaper-image');
      document.querySelector('.custom-wallpaper-layer')?.remove();
      return;
    }
    root.style.setProperty('--custom-wallpaper-image', `url("${value.replace(/"/g, '%22')}")`);
    root.style.setProperty('--custom-wallpaper-x', `${Math.max(0, Math.min(100, Number(config.x) || 50))}%`);
    root.style.setProperty('--custom-wallpaper-y', `${Math.max(0, Math.min(100, Number(config.y) || 50))}%`);
    root.style.setProperty('--custom-wallpaper-zoom', String(Math.max(1, Math.min(2, Number(config.zoom) || 1))));
    root.classList.add('custom-wallpaper');
    ensureLayer();
  }
  function readAsDataURL(blob) {
    return new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => resolve(String(reader.result || ''));
      reader.onerror = () => reject(new Error('无法读取图片'));
      reader.readAsDataURL(blob);
    });
  }
  function loadImage(file) {
    return new Promise((resolve, reject) => {
      const image = new Image();
      const url = URL.createObjectURL(file);
      image.onload = () => { URL.revokeObjectURL(url); resolve(image); };
      image.onerror = () => { URL.revokeObjectURL(url); reject(new Error('无法解析这张图片')); };
      image.src = url;
    });
  }
  function canvasBlob(canvas, quality) {
    return new Promise(resolve => canvas.toBlob(resolve, 'image/jpeg', quality));
  }
  async function prepare(file) {
    if (!file || !String(file.type || '').startsWith('image/')) throw new Error('请选择图片文件');
    if (file.type === 'image/svg+xml') throw new Error('暂不支持 SVG 壁纸');
    const image = await loadImage(file);
    const scale = Math.min(1, MAX_EDGE / Math.max(image.naturalWidth, image.naturalHeight));
    const canvas = document.createElement('canvas');
    canvas.width = Math.max(1, Math.round(image.naturalWidth * scale));
    canvas.height = Math.max(1, Math.round(image.naturalHeight * scale));
    canvas.getContext('2d').drawImage(image, 0, 0, canvas.width, canvas.height);
    let quality = 0.88;
    let blob = await canvasBlob(canvas, quality);
    while (blob && blob.size > MAX_BYTES && quality > 0.5) {
      quality -= 0.08;
      blob = await canvasBlob(canvas, quality);
    }
    if (!blob) throw new Error('壁纸处理失败');
    if (blob.size > MAX_BYTES) throw new Error('图片仍然过大，请换一张尺寸较小的图片');
    return readAsDataURL(blob);
  }
  function localValue(target) {
    const image = ownImage(target);
    let config = null;
    try { config = JSON.parse(localStorage.getItem(configKey(target)) || 'null'); } catch {}
    if (!image && !config) return null;
    return { image, x: 50, y: 50, zoom: 1, iconColor: '', ...(config || {}) };
  }
  function applyRemoteTarget(target, value) {
    if (!TARGETS.includes(target)) return;
    if (!value || value.deleted) {
      localStorage.removeItem(imageKey(target));
      localStorage.removeItem(configKey(target));
      return;
    }
    if (value.image) localStorage.setItem(imageKey(target), value.image);
    else localStorage.removeItem(imageKey(target));
    localStorage.setItem(configKey(target), JSON.stringify({ x: value.x, y: value.y, zoom: value.zoom, iconColor: value.iconColor || '' }));
  }
  async function fetchRemote(applyValues = true) {
    const response = await fetch('/api/appearance', { cache: 'no-store' });
    if (!response.ok) throw new Error(`壁纸同步失败（${response.status}）`);
    const state = await response.json();
    syncRevision = Number(state.revision || 0);
    syncEnabled = syncRevision > 0;
    syncError = '';
    if (applyValues && syncEnabled) {
      Object.entries(state.targets || {}).forEach(([target, value]) => applyRemoteTarget(target, value));
      apply(get(), getConfig());
      window.dispatchEvent(new CustomEvent('wallpapersync', { detail: { revision: syncRevision } }));
    }
    return state;
  }
  async function pushTarget(target, deleted = false) {
    if (!TARGETS.includes(target)) throw new Error('不支持这个页面的壁纸同步');
    const value = deleted ? { deleted: true } : localValue(target);
    if (!value) return null;
    const response = await fetch('/api/appearance', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'If-Match': `"appearance-${syncRevision}"` },
      body: JSON.stringify({ target, appearance: value })
    });
    const state = await response.json().catch(() => ({}));
    if (response.status === 409) {
      syncRevision = Number(state.revision || syncRevision);
      throw new Error('其他设备刚刚更新了壁纸，请重新同步');
    }
    if (!response.ok) throw new Error(state.error || `壁纸同步失败（${response.status}）`);
    syncRevision = Number(state.revision || syncRevision + 1);
    syncEnabled = true;
    syncError = '';
    return state;
  }
  function autoPush(target, deleted = false) {
    if (!syncEnabled) return;
    pushTarget(target, deleted).catch(error => {
      syncError = String(error?.message || error);
      window.dispatchEvent(new CustomEvent('wallpapersyncerror', { detail: { error: syncError } }));
    });
  }
  async function migrateLocal() {
    await fetchRemote(false);
    const configured = TARGETS.filter(target => localValue(target));
    if (!configured.length) throw new Error('这台设备没有可同步的壁纸');
    for (const target of configured) await pushTarget(target);
    apply(get(), getConfig());
    window.dispatchEvent(new CustomEvent('wallpapersync', { detail: { revision: syncRevision } }));
    return { revision: syncRevision, count: configured.length };
  }
  async function setFile(file, config = { x: 50, y: 50, zoom: 1 }, target = pageKey()) {
    const value = await prepare(file);
    try { localStorage.setItem(imageKey(target), value); localStorage.setItem(configKey(target), JSON.stringify(config)); }
    catch { throw new Error('浏览器存储空间不足，请换一张更小的图片'); }
    if (target === 'all' || target === pageKey()) apply(get(), getConfig());
    window.dispatchEvent(new CustomEvent('wallpaperchange', { detail: { value } }));
    autoPush(target);
    return value;
  }
  function setConfig(config, target = pageKey()) {
    localStorage.setItem(configKey(target), JSON.stringify(config));
    if (target === 'all' || target === pageKey()) apply(get(), getConfig());
    autoPush(target);
  }
  function reset(target = pageKey()) {
    localStorage.removeItem(imageKey(target));
    localStorage.removeItem(configKey(target));
    apply(get(), getConfig());
    window.dispatchEvent(new CustomEvent('wallpaperchange', { detail: { value: '' } }));
    autoPush(target, true);
  }
  const ready = fetchRemote(true).catch(error => { syncError = String(error?.message || error); });
  window.WallpaperManager = {
    apply, get, getConfig, ownImage, pageKey, setFile, setConfig, reset, ready, migrateLocal,
    syncFromServer: () => fetchRemote(true),
    getSyncStatus: () => ({ revision: syncRevision, enabled: syncEnabled, error: syncError })
  };
  apply();
  document.addEventListener('DOMContentLoaded', () => {
    new MutationObserver(() => apply(get(), getConfig())).observe(document.body, { attributes: true, attributeFilter: ['class'] });
  }, { once: true });
})();
