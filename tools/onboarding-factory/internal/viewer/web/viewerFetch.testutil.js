// Response-shaped fetch answers shared by the viewer's wiring tests
// (viewer.profile, viewer.recordingsError, viewer.rejections), built once here
// instead of copied into each file. Not a test file: vitest only collects
// `*.test.js`.

// json answers a fetch with a 200 and `body`, readable as JSON or as text.
export function json(body) {
  return Promise.resolve({
    ok: true,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(JSON.stringify(body)),
    headers: { get: () => null },
  })
}

// notOk answers a fetch with a non-2xx `status` whose body is `text`.
export function notOk(status, text) {
  return Promise.resolve({
    ok: false,
    status,
    json: () => Promise.resolve(null),
    text: () => Promise.resolve(text),
    headers: { get: () => null },
  })
}
