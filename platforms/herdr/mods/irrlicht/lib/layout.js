// The panel's rows grouped and ordered the way the macOS app's session list
// is, as plain data so the order main.js draws can be tested without Herdr.
//
// The Swift list draws the daemon's groups (SessionListView.swift:460) in a
// remembered order, and each group's rows in the order the daemon sent them
// (GroupView.swift:144). Here a matched row goes in its session's top-level
// daemon group (sessions.js), and every agent no session matched goes in one
// trailing group.

// OTHER_GROUP_KEY keys the trailing group. It cannot collide with a daemon
// group's name: "unknown" is a real one (sessions.js), and no folder name
// holds a NUL.
export const OTHER_GROUP_KEY = '\u0000other';
export const OTHER_GROUP_NAME = 'Other agents';

// rememberGroups mirrors SessionManager.orderedGroups
// (SessionManager+GroupComposition.swift:431-459): a name seen for the first
// time is appended to the remembered order, in the order this poll lists it;
// a name never leaves it, so a group that comes back takes its old place; and
// the groups shown are sorted by it. The Swift app keeps that order in
// UserDefaults and lets the person move groups with its chevrons; here it
// lives as long as the panel copy, and nothing moves a group.
//   names       this poll's group names, in payload order, each once
//   remembered  the order so far (not changed)
// Returns { ordered, remembered }, where remembered is the same array when no
// name was new.
export function rememberGroups(names, remembered) {
  const seen = new Set(remembered);
  const firstSeen = names.filter((n) => !seen.has(n) && seen.add(n));
  const next = firstSeen.length > 0 ? remembered.concat(firstSeen) : remembered;
  const index = new Map();
  next.forEach((n, i) => {
    if (!index.has(n)) index.set(n, i);
  });
  const ordered = [...names].sort((a, b) => index.get(a) - index.get(b));
  return { ordered, remembered: next };
}

// countText is GroupView's count (GroupView.swift:116-117): "1 session",
// "2 sessions".
export function countText(n, noun) {
  return `${n} ${noun}${n === 1 ? '' : 's'}`;
}

// byGroup collects matched rows by group name, keeping the rows' order and
// the order the names first appear in.
function byGroup(rows) {
  const groups = new Map();
  for (const r of rows) {
    if (!groups.has(r.group)) groups.set(r.group, []);
    groups.get(r.group).push(r);
  }
  return groups;
}

function projectGroup(name, rows) {
  return { key: name, name, gastown: rows[0].gastown === true, count: countText(rows.length, 'session'), rows };
}

function otherGroup(rows) {
  return { key: OTHER_GROUP_KEY, name: OTHER_GROUP_NAME, gastown: false, count: countText(rows.length, 'agent'), rows };
}

// numbered gives every row its place in the panel, from 1, across groups.
// That number is drawn in the row and is what `focus-<n>` and the tooltip
// count, so the three always agree with what is on screen. (The Swift row's
// number restarts in each group, GroupView.swift:147; here it is the
// command's handle, so it runs on.)
function numbered(groups) {
  let n = 0;
  return groups.map((g) => ({ ...g, rows: g.rows.map((r) => ({ ...r, n: ++n })) }));
}

// panelLayout groups and orders panelRows' rows:
//   - matched rows by their session's top-level daemon group, groups in the
//     remembered order (rememberGroups), rows in payload order within each;
//   - then one trailing group of the agents no session matched, in Herdr's
//     order. Before the daemon has answered, that is every agent.
// Returns { groups: [{ key, name, gastown, count, rows }], rows (all of them,
// numbered, in display order), remembered (pass it back next time) }.
export function panelLayout(rows, remembered = []) {
  const matched = rows.filter((r) => r.matched).sort((a, b) => a.order - b.order);
  const sorted = byGroup(matched);
  const names = rememberGroups([...sorted.keys()], remembered);
  const groups = names.ordered.map((name) => projectGroup(name, sorted.get(name)));
  const others = rows.filter((r) => !r.matched);
  if (others.length > 0) groups.push(otherGroup(others));
  const shown = numbered(groups);
  return { groups: shown, rows: shown.flatMap((g) => g.rows), remembered: names.remembered };
}

// headerSummary is the Swift header's session glyphs
// (SessionListView.swift:1246-1262): a state icon per row for up to three
// rows, else a count. Nothing when there is no row.
export function headerSummary(layout) {
  const rows = layout.rows;
  if (rows.length > 3) return { states: [], text: countText(rows.length, 'agent') };
  return { states: rows.map((r) => r.state), text: '' };
}
