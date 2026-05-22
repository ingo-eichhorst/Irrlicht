// Minimum DOM required for irrlicht.js top-level wiring
document.body.innerHTML = `
  <button id="theme-toggle"></button>
  <button id="view-mode-cycle">Context</button>
  <button id="settings-toggle"></button>
  <button id="settings-close"></button>
  <div id="settings-backdrop"></div>
  <div id="settings-providers"></div>
  <div id="session-list"></div>
  <div id="app-version"></div>
  <div id="empty-state"></div>
  <div id="ws-dot" class="ws-dot"></div>
  <span id="ws-label"></span>
  <div id="quota-chips"></div>
  <div id="app-state-icons"></div>
  <div id="gt-container" style="display:none"></div>
  <div id="connection-banner"></div>
  <div id="settings-perm-note"></div>
`;

// jsdom has no matchMedia — provide a stub
if (!window.matchMedia) {
  window.matchMedia = (query) => ({
    matches: false,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
  });
}

// Stub WebSocket so connect() is a no-op
class MockWebSocket {
  constructor() {}
  addEventListener() {}
  send() {}
  close() {}
}
global.WebSocket = MockWebSocket;

// Stub fetch — all call sites have .catch(() => null)
global.fetch = () => Promise.reject(new Error('no server'));

// Stub Notification
global.Notification = class {
  static permission = 'default';
  static requestPermission() { return Promise.resolve('default'); }
  constructor() {}
};
