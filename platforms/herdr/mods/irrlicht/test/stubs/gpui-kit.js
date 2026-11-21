// A stand-in for Herdr's `gpui-kit` module, so test/main.render.test.js can
// load main.js under node (vitest.config.js aliases the bare specifier here).
// Every element records the builder calls made on it, in order, and its
// children, so a test can read back what main.js asked Herdr to draw. It
// checks no layout: Herdr's own `herdr-app check` and a look at the window
// are what do that.

// View is the class a mod's default export extends; Herdr calls the mod's own
// init() and render() on it, so the stand-in needs nothing of its own but a
// way to tell it from the real one.
export class View {
  get stub() {
    return true;
  }
}

// el is one element: `kind` ("div", "svg", ...), `arg` (an svg's path), the
// `calls` made on it and its children (`kids`; not `children`, which is a
// builder method). `when(cond, fn)` runs as gpui runs it; any other method is
// recorded and returns the element.
export function el(kind, arg) {
  const node = { kind, arg, calls: [], kids: [] };
  const proxy = new Proxy(node, {
    get(target, prop) {
      if (prop in target) return target[prop];
      if (prop === 'then') return undefined;
      return (...args) => record(proxy, node, prop, args);
    },
  });
  return proxy;
}

// The builder methods that add children rather than set something.
const ADDS = new Map([
  ['child', (node, args) => node.kids.push(args[0])],
  ['children', (node, args) => node.kids.push(...args[0])],
]);

function record(proxy, node, prop, args) {
  if (prop === 'when') return args[0] ? args[1](proxy) : proxy;
  const add = ADDS.get(prop);
  if (add) add(node, args);
  else node.calls.push([prop, ...args]);
  return proxy;
}

export const div = () => el('div');
export const svg = (path) => el('svg', path);
export const image = (path) => el('image', path);
