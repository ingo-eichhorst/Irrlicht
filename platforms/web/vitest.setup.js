// Minimum DOM required for irrlicht.js top-level wiring
document.body.innerHTML = `
  <header></header>
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
  <button id="settings-review-permissions"></button>
  <div id="permissions-backdrop">
    <h2 id="permissions-title"></h2>
    <p id="permissions-intro"></p>
    <div id="permissions-body"></div>
    <button id="permissions-apply"></button>
  </div>
`;

// jsdom has no canvas — give paintRowHistory the minimal 2D context it uses
// so tests that render session rows don't trip an unhandled rejection.
HTMLCanvasElement.prototype.getContext = () => ({
  setTransform: () => {},
  clearRect: () => {},
  fillRect: () => {},
});

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

// Stub WebSocket — supports ws.onopen/onmessage/onerror/onclose assignments
// (irrlicht.js uses property assignment, not addEventListener). Tests can
// call lastMockWebSocket.simulateOpen() / simulateMessage(data) to exercise
// the connect() and reconnect paths.
class MockWebSocket {
  constructor(url) {
    this.url = url;
    this.onopen = null;
    this.onmessage = null;
    this.onerror = null;
    this.onclose = null;
    this.readyState = 0; // CONNECTING
    global.lastMockWebSocket = this;
  }
  addEventListener() {}
  send() {}
  close() {
    this.readyState = 3; // CLOSED
    if (this.onclose) this.onclose({ code: 1000, reason: '' });
  }
  simulateOpen() {
    this.readyState = 1; // OPEN
    if (this.onopen) this.onopen({});
  }
  simulateMessage(data) {
    const payload = typeof data === 'string' ? data : JSON.stringify(data);
    if (this.onmessage) this.onmessage({ data: payload });
  }
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
