// Formatting helpers for the panel. Pure and DOM-free: they run in the hooks
// module's environment (no DOM, no Node) and under vitest alike.

// formatTokens is compact on purpose (the pane is a narrow column): 950,
// 4.2K, 177K, 1.2M, 12M.
export function formatTokens(n) {
  if (!Number.isFinite(n) || n <= 0) return '';
  if (n < 1000) return String(Math.round(n));
  if (n < 10_000) return (n / 1000).toFixed(1) + 'K';
  if (n < 1_000_000) return Math.round(n / 1000) + 'K';
  if (n < 10_000_000) return (n / 1_000_000).toFixed(1) + 'M';
  return Math.round(n / 1_000_000) + 'M';
}

// formatPercent prints a context-window fill to one decimal, or nothing when
// the daemon reported none.
export function formatPercent(p) {
  if (!Number.isFinite(p) || p <= 0) return '';
  return p.toFixed(1) + '%';
}

// shortModel turns a model id into what fits a row: the vendor prefix and a
// dated snapshot suffix dropped, a trailing major-minor pair joined with a
// dot (claude-opus-5-5 → opus-5.5, claude-sonnet-4-5-20250929 → sonnet-4.5);
// an id already dotted (gpt-5.4) is left as it is.
export function shortModel(id) {
  if (!id || id === 'unknown') return '';
  return id
    .replace(/^claude-/, '')
    .replace(/-\d{8}$/, '')
    .replace(/-(\d+)-(\d+)$/, '-$1.$2');
}

// fmtDuration mirrors the dashboard's formatters.js fmtDuration.
export function fmtDuration(secs) {
  if (!Number.isFinite(secs) || secs < 0) return '';
  secs = Math.floor(secs);
  const h = Math.floor(secs / 3600);
  const m = Math.floor((secs % 3600) / 60);
  const s = secs % 60;
  if (h > 0) return h + 'h' + m + 'm';
  if (m > 0) return m + 'm' + s + 's';
  return s + 's';
}

// formatUsd mirrors the dashboard's quotaChips.js formatUsageCost: $0,
// <$0.01, $12.34, $123.
export function formatUsd(cost) {
  if (!cost || cost <= 0) return '$0';
  if (cost < 0.01) return '<$0.01';
  if (cost >= 100) return '$' + Math.round(cost);
  return '$' + cost.toFixed(2);
}

// oneLine folds whitespace (newlines included) to single spaces and cuts the
// text at `max` characters with an ellipsis, cutting by code point so an
// emoji is never split into a lone surrogate.
export function oneLine(text, max) {
  if (typeof text !== 'string') return '';
  const flat = text.replace(/\s+/g, ' ').trim();
  const chars = Array.from(flat);
  if (chars.length <= max) return flat;
  return chars.slice(0, Math.max(0, max - 1)).join('').trimEnd() + '…';
}

// quotaBar draws `width` cells, filled in proportion to `percent`.
export function quotaBar(percent, width) {
  const p = Math.min(100, Math.max(0, percent || 0));
  const filled = Math.round((p / 100) * width);
  return '█'.repeat(filled) + '░'.repeat(width - filled);
}

// fitRow lays one session row out in `width` columns. The row reads
//   ● <branch> <tokens> <context> <model> [adapter] ← here
// and when it is too wide the branch is shortened first (it is the one free
// text), then the model, then the context figure: the state dot, the adapter
// tag and the here-marker are what the row is for, so they go last.
// Returns the text after the dot, the dot itself drawn by the caller in the
// state's colour.
export function fitRow(row, width) {
  const tail = [];
  if (row.adapterTag) tail.push(row.adapterTag);
  if (row.isHere) tail.push('← here');
  const room = Math.max(0, width - 2); // "● " is drawn ahead of this text
  const compose = (branch, keep) =>
    [branch, ...keep.map(k => row[k]), ...tail].filter(Boolean).join(' ');

  const branch = row.branch || '—';
  let keep = ['tokens', 'context', 'model'].filter(k => row[k]);
  const dropOrder = ['model', 'context', 'tokens'];
  for (;;) {
    const fixed = compose('', keep);
    const branchRoom = room - (fixed ? fixed.length + 1 : 0);
    if (branchRoom >= Math.min(branch.length, 6) || keep.length === 0) {
      return compose(shorten(branch, Math.max(1, branchRoom)), keep);
    }
    const drop = dropOrder.find(k => keep.includes(k));
    keep = keep.filter(k => k !== drop);
  }
}

function shorten(text, max) {
  const chars = Array.from(text);
  if (chars.length <= max) return text;
  if (max <= 1) return '…';
  return chars.slice(0, max - 1).join('') + '…';
}
