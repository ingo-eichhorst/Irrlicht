// domReconcile.js — generic keyed DOM-list reconciliation. reconcile()
// patches a parent's children to match a desired item list (by key),
// reusing/moving/creating/removing elements instead of a full re-render —
// this is what lets render() update in place instead of rebuilding the
// session list every tick. paintRowNum is a small row-specific helper
// that lives alongside it. No app state — pure DOM manipulation.

// --- DOM Reconciliation ---

// The slot immediately after prevNode (or the parent's first child when
// there is no prevNode yet) — where the next reconciled item belongs.
function expectedRef(parent, prevNode) {
  return prevNode ? prevNode.nextSibling : parent.firstChild;
}

// Inserts/moves `el` so it sits directly before `ref` (or at the end of
// `parent` when there is no ref), matching native `before`/`appendChild`.
function insertBeforeRef(parent, el, ref) {
  if (ref) ref.before(el);
  else parent.appendChild(el);
}

// Keyed reconcile: patches children of `parent` to match `items`.
export function reconcile(parent, items, keyFn, createFn, updateFn) {
  const existingByKey = new Map();
  for (const child of parent.children) {
    const k = child.dataset.key;
    if (k) existingByKey.set(k, child);
  }

  const desiredKeys = new Set();
  let prevNode = null;

  for (const item of items) {
    const key = keyFn(item);
    desiredKeys.add(key);
    let el = existingByKey.get(key);

    if (el) {
      updateFn(el, item);
      // Move to correct position if needed
      const expected = expectedRef(parent, prevNode);
      if (el !== expected) insertBeforeRef(parent, el, expected);
    } else {
      el = createFn(item);
      el.dataset.key = key;
      insertBeforeRef(parent, el, expectedRef(parent, prevNode));
    }
    prevNode = el;
  }

  // Remove orphans
  for (const [key, el] of existingByKey) {
    if (!desiredKeys.has(key)) {
      parent.removeChild(el);
    }
  }
}

// Writes the row-num slot: if updateSessionRow flagged an icon override
// (agent.icon set, agent.role empty), show the icon glyph instead of the
// numeric agent number. Matches macOS at SessionListView.swift:469-479.
export function paintRowNum(el, num) {
  const numEl = el.querySelector('.row-num');
  if (!numEl) return;
  const icon = numEl.dataset.iconOverride || '';
  const desired = icon || String(num);
  if (numEl.textContent !== desired) numEl.textContent = desired;
}

