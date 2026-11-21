// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

(() => {
  'use strict';
  const current = document.body.dataset.kind;
  const base = new TextDecoder().decode(Uint8Array.from(atob(document.body.dataset.base), c => c.charCodeAt(0)));
  const select = document.getElementById('endpoint-kind');
  const input = document.getElementById('endpoint-url');
  const editor = document.getElementById('editor');
  const notice = document.getElementById('notice');
  const sessions = {admin: {url: '', values: new Map()}, storefront: {url: '', values: new Map()}};
  const currentPath = location.pathname.replace(/\/(admin|storefront)\/playground$/, '/$1/graphql');
  sessions[current].url = new URL(currentPath, location.href).href;
  select.value = current;
  let frame;

  const sensitiveKey = key => /header|auth|token|cookie/i.test(key);
  function editorValue(value) {
    // GraphiQL history can contain nested request headers as well as a
    // dedicated headers key. Keep documents/settings, strip credential state.
    try { return JSON.stringify(JSON.parse(value), (key, entry) => sensitiveKey(key) ? undefined : entry); }
    catch { return value; }
  }

  // Only editor/history state is shared with a replacement frame. Headers and
  // credentials never enter storage and are reset even when returning to a URL.
  window.gitstorePlaygroundStorage = kind => ({
    getItem(key) { return sensitiveKey(key) ? null : sessions[kind].values.get(key) ?? null; },
    setItem(key, value) { if (!sensitiveKey(key)) sessions[kind].values.set(key, editorValue(value)); },
    removeItem(key) { sessions[kind].values.delete(key); },
    clear() { sessions[kind].values.clear(); },
    get length() { return sessions[kind].values.size; },
    key(index) { return Array.from(sessions[kind].values.keys())[index] ?? null; }
  });

  function disconnect() {
    if (!frame) return;
    const child = frame.contentWindow;
    if (child) {
      child.ReactDOM?.unmountComponentAtNode(child.document.getElementById('graphiql'));
      child.gitstoreDispose?.();
    }
    frame.remove();
    frame = undefined;
  }

  function connect() {
    disconnect();
    const kind = select.value;
    const value = input.value.trim();
    if (!value) {
      notice.textContent = 'Enter the ' + kind + ' URL. Private peer addresses are not discovered or guessed.';
      return;
    }
    let target;
    try {
      target = new URL(value);
      if (!['http:', 'https:'].includes(target.protocol) || target.username || target.password || target.hash || target.search || !target.pathname.endsWith('/' + kind + '/graphql')) throw new Error();
    } catch {
      notice.textContent = 'Use an HTTP(S) URL ending in /' + kind + '/graphql, without embedded credentials.';
      return;
    }
    if (sessions[kind].url !== target.href) sessions[kind].values.clear();
    sessions[kind].url = target.href;
    const literal = v => JSON.stringify(v).replaceAll('<', '\\u003c');
    const subscription = new URL(target.href);
    subscription.protocol = target.protocol === 'https:' ? 'wss:' : 'ws:';
    const lifetime = `const lifetime = new AbortController(); const sockets = new Set();
      const NativeSocket = window.WebSocket;
      window.WebSocket = class extends NativeSocket { constructor(...args) { super(...args); sockets.add(this); this.addEventListener('close', () => sockets.delete(this)); } };
      window.gitstoreDispose = () => { lifetime.abort(); for (const socket of sockets) socket.close(1000, 'Endpoint changed'); sockets.clear(); };
      window.addEventListener('pagehide', window.gitstoreDispose);
      const fetcher = GraphiQL.createFetcher({url, subscriptionUrl, headers: undefined,
        fetch: (request, options = {}) => fetch(request, {...options, credentials: 'omit', signal: options.signal ? AbortSignal.any([options.signal, lifetime.signal]) : lifetime.signal})});`;
    let html = base
	  .replace(/<title>[^<]*<\/title>/, '<title>GitStore ' + kind + '</title>')
      .replace(/const url =[^;]*;/, 'const url = ' + literal(target.href) + ';')
      .replace(/const subscriptionUrl =[^;]*;/, 'const subscriptionUrl = ' + literal(subscription.href) + ';')
      .replace('const fetcher = GraphiQL.createFetcher({ url, subscriptionUrl, headers: fetcherHeaders });', lifetime)
      .replace('shouldPersistHeaders: true', 'shouldPersistHeaders: false')
      .replace(/storage: new PrefixedStorage\('[^']*'\)/, 'storage: window.parent.gitstorePlaygroundStorage(' + literal(kind) + ')');
    frame = document.createElement('iframe');
    frame.title = 'GitStore ' + kind + ' GraphQL editor';
    frame.setAttribute('sandbox', 'allow-scripts allow-same-origin');
    frame.srcdoc = html;
    editor.replaceChildren(frame);
    notice.textContent = kind === 'storefront'
      ? 'Storefront requests return unavailable until publication support exists. Headers are empty; cross-origin access requires configured CORS.'
      : 'Admin editor connected. Headers are empty; authentication is required for introspection.';
  }

  select.addEventListener('change', () => { input.value = sessions[select.value].url; connect(); });
  document.getElementById('endpoint-form').addEventListener('submit', event => { event.preventDefault(); connect(); });
  input.value = sessions[current].url;
  connect();
})();
