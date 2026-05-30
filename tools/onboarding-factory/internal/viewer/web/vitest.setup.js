// Minimum DOM required for viewer.js top-level wiring
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
