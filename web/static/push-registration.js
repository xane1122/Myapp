(function () {
  'use strict';

  if (window.__myappPushRegistrationStarted) return;
  window.__myappPushRegistrationStarted = true;

  const USER_ID_KEY = 'myapp.push.user_id';
  const SERVICE_WORKER_VERSION = '20260917-1';
  const GESTURE_EVENTS = ['pointerdown', 'touchend', 'click', 'keydown'];
  let waitingForGesture = false;

  function supported() {
    return window.isSecureContext && 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window;
  }

  function isIOS() {
    return /iPhone|iPad|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
  }

  function userID() {
    let value = '';
    try { value = localStorage.getItem(USER_ID_KEY) || ''; } catch (_) {}
    if (value) return value;
    value = window.crypto && typeof window.crypto.randomUUID === 'function'
      ? window.crypto.randomUUID()
      : `web-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
    try { localStorage.setItem(USER_ID_KEY, value); } catch (_) {}
    return value;
  }

  function base64URLBytes(value) {
    const padded = value + '='.repeat((4 - value.length % 4) % 4);
    const raw = atob(padded.replace(/-/g, '+').replace(/_/g, '/'));
    const bytes = new Uint8Array(raw.length);
    for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
    return bytes;
  }

  async function registerSubscription() {
    if (!supported()) return { ok: false, reason: 'unsupported' };
    if (Notification.permission !== 'granted') return { ok: false, reason: `permission-${Notification.permission}` };

    const infoResponse = await fetch('/api/push', { cache: 'no-store' });
    if (!infoResponse.ok) throw new Error(`push public key request failed: ${infoResponse.status}`);
    const info = await infoResponse.json();
    if (!info.public_key) throw new Error('push public key missing');

    const registration = await navigator.serviceWorker.register(`/sw.js?v=${SERVICE_WORKER_VERSION}`, { updateViaCache: 'none' });
    await registration.update().catch(() => {});
    let subscription = await registration.pushManager.getSubscription();
    if (!subscription) {
      subscription = await registration.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: base64URLBytes(info.public_key),
      });
    }
    const serialized = subscription.toJSON();
    const response = await fetch('/api/push', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        user_id: userID(),
        endpoint: subscription.endpoint,
        keys: serialized.keys,
      }),
    });
    if (!response.ok) throw new Error(`push subscription request failed: ${response.status}`);
    return { ok: true };
  }

  function removeGestureListeners() {
    GESTURE_EVENTS.forEach(eventName => window.removeEventListener(eventName, requestPermissionFromGesture, true));
    waitingForGesture = false;
  }

  async function requestPermissionFromGesture() {
    if (!waitingForGesture) return;
    removeGestureListeners();
    try {
      if (await Notification.requestPermission() === 'granted') await registerSubscription();
    } catch (_) {}
  }

  function waitForUserGesture() {
    if (waitingForGesture || Notification.permission !== 'default') return;
    waitingForGesture = true;
    GESTURE_EVENTS.forEach(eventName => window.addEventListener(eventName, requestPermissionFromGesture, true));
  }

  async function start() {
    if (!supported()) return;
    try {
      if (Notification.permission === 'granted') {
        await registerSubscription();
        return;
      }
      if (Notification.permission === 'denied') return;
      if (isIOS()) {
        // iOS only accepts notification permission requests from a user gesture.
        waitForUserGesture();
        return;
      }
      const permission = await Notification.requestPermission();
      if (permission === 'granted') await registerSubscription();
      else if (permission === 'default') waitForUserGesture();
    } catch (_) {
      waitForUserGesture();
    }
  }

  window.MyAppPushRegistration = { start, registerSubscription };
  start();
}());
