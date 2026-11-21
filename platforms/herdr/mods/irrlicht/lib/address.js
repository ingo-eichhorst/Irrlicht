// The daemon's address, resolved the way a separate client process resolves it
// in Go: a port of daemonaddr.ClientURL's ladder
// (core/pkg/daemonaddr/daemonaddr.go: resolveClient, fixedPortOf,
// publishedAddr, AddrFilePath). Pure: main.js performs the reads through
// Herdr's `exec` and `readTextFile` and hands them in through `io`, so this
// file runs under vitest too.
//
// The ladder, in Go's order:
//   1. IRRLICHT_BIND_ADDR, honored only when it is a host:port whose port is a
//      fixed one (1..65535). The host is ignored: the client always dials
//      localhost (ClientURL's doc says why).
//   2. The address the running daemon published, read from
//      $IRRLICHT_HOME/irrlichd.addr, else $HOME/.local/share/irrlicht/irrlichd.addr,
//      else /tmp/irrlicht/irrlichd.addr when HOME is empty. Only a regular file
//      (not a symlink) is read, its first 64 bytes, trimmed, and it must name a
//      fixed port too.
//   3. The default port, 7837.
//
// The same ladder is ported for the Claude Code panel (#2139). A Herdr mod is
// one folder that people copy on its own, so this copy cannot import that one.

export const DEFAULT_PORT = 7837;
const ADDR_FILE_NAME = 'irrlichd.addr';
const MAX_ADDR_FILE_BYTES = 64;

// splitHostPort mirrors Go's net.SplitHostPort: the port is what follows the
// last colon, a bracketed host must close right before it, and a host with a
// colon of its own (an unbracketed IPv6) is refused. Returns { host, port } or
// null where Go returns an error.
function splitHostPort(hostport) {
  if (typeof hostport !== 'string') return null;
  const i = hostport.lastIndexOf(':');
  if (i < 0) return null; // missing port in address
  const span = hostport.startsWith('[') ? bracketedHostSpan(hostport, i) : plainHostSpan(hostport, i);
  if (!span || hasStrayBracket(hostport, span)) return null;
  return { host: span.host, port: hostport.slice(i + 1) };
}

// hasStrayBracket is SplitHostPort's last two checks: a '[' at or after j, or
// a ']' at or after k.
function hasStrayBracket(hostport, { j, k }) {
  return hostport.slice(j).includes('[') || hostport.slice(k).includes(']');
}

// bracketedHostSpan is SplitHostPort's `hostport[0] == '['` branch: the first
// ']' must sit right before the last colon `i`.
function bracketedHostSpan(hostport, i) {
  const end = hostport.indexOf(']');
  if (end < 0) return null; // missing ']' in address
  if (end + 1 === hostport.length) return null; // missing port
  if (end + 1 !== i) return null; // too many colons, or ']' not followed by the last ':'
  return { host: hostport.slice(1, end), j: 1, k: end + 1 };
}

// plainHostSpan is SplitHostPort's unbracketed branch: the host is everything
// before the last colon, and must hold no colon of its own.
function plainHostSpan(hostport, i) {
  const host = hostport.slice(0, i);
  if (host.includes(':')) return null; // too many colons in address
  return { host, j: 0, k: 0 };
}

// goAtoi mirrors strconv.Atoi for base-10 input: an optional sign, then one
// or more ASCII digits and nothing else. Returns a number, or null where Go
// returns an error.
function goAtoi(s) {
  if (typeof s !== 'string' || !/^[+-]?\d+$/.test(s)) return null;
  return Number(s);
}

// fixedPortOf mirrors daemonaddr.fixedPortOf: the port of a host:port when it
// is one a client can be pointed at (1..65535), else null.
export function fixedPortOf(addr) {
  const parts = splitHostPort(addr);
  if (!parts) return null;
  const n = goAtoi(parts.port);
  return n !== null && n > 0 && n <= 65535 ? n : null;
}

// isGoSpace is unicode.IsSpace, not String.prototype.trim's set: they differ on
// U+0085 (Go trims it, JS does not) and U+FEFF (JS trims it, Go does not).
const GO_SPACE_RUNS = [[0x09, 0x0d], [0x2000, 0x200a]];
const GO_SPACE_POINTS = new Set([0x20, 0x85, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000]);

function isGoSpace(ch) {
  const c = ch.codePointAt(0);
  return GO_SPACE_POINTS.has(c) || GO_SPACE_RUNS.some(([lo, hi]) => c >= lo && c <= hi);
}

// goTrimSpace mirrors strings.TrimSpace.
function goTrimSpace(s) {
  const chars = Array.from(s);
  let start = 0;
  let end = chars.length;
  while (start < end && isGoSpace(chars[start])) start++;
  while (end > start && isGoSpace(chars[end - 1])) end--;
  return chars.slice(start, end).join('');
}

// cleanPath mirrors path/filepath.Clean on a Unix host (lexical only).
export function cleanPath(p) {
  if (p === '') return '.';
  const rooted = p.startsWith('/');
  const out = [];
  for (const seg of p.split('/')) pushSegment(out, seg, rooted);
  const joined = out.join('/');
  if (rooted) return '/' + joined;
  return joined === '' ? '.' : joined;
}

// pushSegment is one turn of Clean's loop: an empty element (a doubled slash)
// and `.` add nothing, `..` is foldDotDot's, anything else is kept.
function pushSegment(out, seg, rooted) {
  if (seg === '' || seg === '.') return;
  if (seg === '..') foldDotDot(out, rooted);
  else out.push(seg);
}

// foldDotDot is Clean's `..` rule: it removes the element before it unless
// that is a `..` too; with nothing to remove it is kept, except at the root,
// where `/..` is `/`.
function foldDotDot(out, rooted) {
  if (out.length > 0 && out[out.length - 1] !== '..') out.pop();
  else if (!rooted) out.push('..');
}

// addrFilePath mirrors daemonaddr.AddrFilePath. `irrlichtHome` and `home` are
// the values of IRRLICHT_HOME and HOME; unset and empty are the same to Go.
// The /tmp/irrlicht rung is AddrFilePath's own fallback when HOME is empty; the
// file is only ever read, and only a port number is taken from it.
export function addrFilePath({ irrlichtHome, home }) {
  if (irrlichtHome) return cleanPath(irrlichtHome + '/' + ADDR_FILE_NAME);
  if (!home) return cleanPath('/tmp/irrlicht/' + ADDR_FILE_NAME); // NOSONAR (javascript:S5443): AddrFilePath's own fallback, read-only, see above
  return cleanPath(home + '/.local/share/irrlicht/' + ADDR_FILE_NAME);
}

// utf8Length is how many bytes a code point takes in UTF-8.
function utf8Length(codePoint) {
  if (codePoint < 0x80) return 1;
  if (codePoint < 0x800) return 2;
  if (codePoint < 0x10000) return 3;
  return 4;
}

// firstBytes is the text whose UTF-8 encoding is the first `max` bytes of
// `text`'s. A character the cut splits becomes U+FFFD, as a byte that is not
// valid UTF-8 would: Go keeps the stray bytes, TrimSpace leaves them, and the
// port then fails to parse either way. Herdr's runtime has no TextEncoder
// (measured: `typeof TextEncoder` is "undefined" in a live mod), so the
// lengths are counted here.
function firstBytes(text, max) {
  let out = '';
  let used = 0;
  for (const ch of text) {
    const n = utf8Length(ch.codePointAt(0));
    if (used + n > max) return used < max ? out + '�' : out;
    out += ch;
    used += n;
  }
  return out;
}

// addrFromFileText mirrors publishedAddr's read: the first 64 bytes of the
// file, then strings.TrimSpace. A longer file whose first 64 bytes hold an
// address plus whitespace still names that address (Go reads through
// io.LimitReader). `readTextFile` hands the file over as decoded text, so a
// byte that is not valid UTF-8 already arrives as U+FFFD; the daemon writes one
// ASCII host:port line, so only a file it did not write can tell the
// difference.
export function addrFromFileText(text) {
  if (typeof text !== 'string') return '';
  return goTrimSpace(firstBytes(text, MAX_ADDR_FILE_BYTES));
}

// resolveDaemonAddress walks the ladder. `io` carries the reads main.js
// performs, each a function resolving a value or undefined; any of them may
// reject, which counts as "not there":
//   bindAddr()           → IRRLICHT_BIND_ADDR
//   irrlichtHome()       → IRRLICHT_HOME
//   home()               → HOME
//   isRegularFile(path)  → true only for a regular file that is not a symlink
//                          (publishedAddr's os.Lstat + Mode().IsRegular())
//   read(path)           → the file's text
// Resolves { port, source, addrFile } where source is 'env', 'file' or
// 'default'.
export async function resolveDaemonAddress(io) {
  const settle = async (fn, ...args) => {
    try { return await fn(...args); } catch { return undefined; }
  };

  const fromEnv = fixedPortOf((await settle(io.bindAddr)) || '');
  if (fromEnv !== null) return { port: fromEnv, source: 'env', addrFile: null };

  const addrFile = addrFilePath({
    irrlichtHome: await settle(io.irrlichtHome),
    home: await settle(io.home),
  });
  if ((await settle(io.isRegularFile, addrFile)) === true) {
    const fromFile = fixedPortOf(addrFromFileText(await settle(io.read, addrFile)));
    if (fromFile !== null) return { port: fromFile, source: 'file', addrFile };
  }
  return { port: DEFAULT_PORT, source: 'default', addrFile };
}

// hostPortOf is what the panel prints: the host it dials and the port.
export function hostPortOf(resolved) {
  return 'localhost:' + resolved.port;
}

// sessionsURL is the endpoint the panel polls. Plain http on purpose
// (SonarQube javascript:S5332): the daemon serves plain HTTP (srv.Serve(tcpL)
// in core/cmd/irrlichd/main.go; a grep for crypto/tls and ServeTLS in that
// package finds nothing), and the panel dials localhost whatever host the
// ladder named, so the request never leaves the machine.
export function sessionsURL(resolved) {
  return 'http://' + hostPortOf(resolved) + '/api/v1/sessions'; // NOSONAR (javascript:S5332): local daemon, plain HTTP only, see above
}
