// Minimum DOM required for viewer.js top-level wiring
// jsdom implements no scrolling, and viewer.js scrolls the active sidebar
// button into view on every navigation. Stubbed here rather than guarded in
// production code: the absence is the test environment's, not the SPA's.
if (typeof Element.prototype.scrollIntoView !== 'function') {
  Element.prototype.scrollIntoView = function scrollIntoViewStub() {}
}

document.body.innerHTML = `
  <div id="scenarios"></div>
  <div id="title"></div>
  <div id="breadcrumb"></div>
  <div id="detail"></div>
`;

// Return empty-but-valid responses so the async init() IIFE doesn't
// throw — it gracefully handles an empty scenarios list.
global.fetch = (url) => {
  if (url === '/api/scenarios') {
    return Promise.resolve({ ok: true, json: () => Promise.resolve([]) });
  }
  return Promise.resolve({
    ok: false,
    json: () => Promise.resolve(null),
    headers: { get: () => null },
  });
};
