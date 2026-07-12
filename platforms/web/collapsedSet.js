// collapsedSet.js — shared persisted-Set-in-localStorage backing for
// collapsedGroups.js and collapsedSummaries.js. Both modules are a thin,
// differently-named public API over the same load/persist/toggle logic
// (SonarQube flagged the two independent copies as duplicated code once
// their swallowed-exception catches were logged identically — issue #901);
// this factory is the single implementation both wrap.
export function makeCollapsedSet(key) {
  function load() {
    try {
      const raw = localStorage.getItem(key);
      if (raw) {
        const parsed = JSON.parse(raw);
        if (Array.isArray(parsed)) return new Set(parsed.filter(s => typeof s === 'string'));
      }
    } catch (e) {
      console.debug(`${key}: failed to load, starting empty`, e);
    }
    return new Set();
  }

  let collapsed = load();

  function persist() {
    try { localStorage.setItem(key, JSON.stringify([...collapsed])); } catch (e) {
      console.debug(`${key}: failed to persist`, e);
    }
  }

  return {
    has: (id) => collapsed.has(id),
    toggle(id) {
      if (collapsed.has(id)) {
        collapsed.delete(id);
      } else {
        collapsed.add(id);
      }
      persist();
    },
    get size() { return collapsed.size; },
    setAll(ids) { collapsed = new Set(ids); persist(); },
    clear() { collapsed = new Set(); persist(); },
  };
}
