// The daemon's address, resolved the way a separate client process resolves it
// in Go: a port of daemonaddr.ClientURL's ladder
// (core/pkg/daemonaddr/daemonaddr.go: resolveClient, fixedPortOf,
// publishedAddr, AddrFilePath). Pure: the hooks module reads the environment
// and the addr file through `$` and hands the values in, because `$.env.get`
// only takes a string literal and this file must run under vitest too.
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

export const DEFAULT_PORT = 7837;
export const ADDR_FILE_NAME = 'irrlichd.addr';
export const MAX_ADDR_FILE_BYTES = 64;

// splitHostPort mirrors Go's net.SplitHostPort byte for byte: the port is
// what follows the last colon, a bracketed host must close right before it,
// and a host with a colon of its own (an unbracketed IPv6) is refused.
// Returns { host, port } or null where Go returns an error.
export function splitHostPort(hostport) {
  if (typeof hostport !== 'string') return null;
  const i = hostport.lastIndexOf(':');
  if (i < 0) return null; // missing port in address
  let host;
  let j = 0;
  let k = 0;
  if (hostport[0] === '[') {
    const end = hostport.indexOf(']');
    if (end < 0) return null; // missing ']' in address
    if (end + 1 === hostport.length) return null; // missing port
    if (end + 1 !== i) return null; // too many colons, or ']' not followed by the last ':'
    host = hostport.slice(1, end);
    j = 1;
    k = end + 1;
  } else {
    host = hostport.slice(0, i);
    if (host.includes(':')) return null; // too many colons in address
  }
  if (hostport.slice(j).includes('[')) return null; // unexpected '['
  if (hostport.slice(k).includes(']')) return null; // unexpected ']'
  return { host, port: hostport.slice(i + 1) };
}

// goAtoi mirrors strconv.Atoi for base-10 input: an optional sign, then one
// or more ASCII digits and nothing else. Returns a number, or null where Go
// returns an error. Values past 2^53 lose precision here, which cannot change
// a verdict: anything that large is far outside 1..65535 either way.
export function goAtoi(s) {
  if (typeof s !== 'string' || !/^[+-]?[0-9]+$/.test(s)) return null;
  return Number(s);
}

// fixedPortOf mirrors daemonaddr.fixedPortOf: the port of a host:port when it
// is one a client can be pointed at (1..65535; 0 asks the OS to pick one and
// names nothing), else null.
export function fixedPortOf(addr) {
  const parts = splitHostPort(addr);
  if (!parts) return null;
  const n = goAtoi(parts.port);
  if (n === null || !(n > 0 && n <= 65535)) return null;
  return n;
}

// isGoSpace is unicode.IsSpace: the Latin-1 spaces plus the White_Space
// property. Not String.prototype.trim's set, which differs on U+0085 (Go trims
// it, JS does not) and U+FEFF (JS trims it, Go does not).
function isGoSpace(ch) {
  const c = ch.codePointAt(0);
  return (c >= 0x09 && c <= 0x0d) || c === 0x20 || c === 0x85 || c === 0xa0 ||
    c === 0x1680 || (c >= 0x2000 && c <= 0x200a) || c === 0x2028 || c === 0x2029 ||
    c === 0x202f || c === 0x205f || c === 0x3000;
}

// goTrimSpace mirrors strings.TrimSpace.
export function goTrimSpace(s) {
  const chars = Array.from(s);
  let start = 0;
  let end = chars.length;
  while (start < end && isGoSpace(chars[start])) start++;
  while (end > start && isGoSpace(chars[end - 1])) end--;
  return chars.slice(start, end).join('');
}

// cleanPath mirrors path/filepath.Clean on a Unix host: lexical only, as Go's
// is (a `..` after a symlink is folded without looking at the link).
export function cleanPath(p) {
  if (p === '') return '.';
  const rooted = p.startsWith('/');
  const out = [];
  for (const seg of p.split('/')) {
    if (seg === '' || seg === '.') continue;
    if (seg === '..') {
      if (out.length > 0 && out[out.length - 1] !== '..') out.pop();
      else if (!rooted) out.push('..');
      continue;
    }
    out.push(seg);
  }
  const joined = out.join('/');
  if (rooted) return '/' + joined;
  return joined === '' ? '.' : joined;
}

// addrFilePath mirrors daemonaddr.AddrFilePath. `irrlichtHome` and `home` are
// the values of IRRLICHT_HOME and HOME (os.UserHomeDir reads $HOME on Unix);
// unset and empty are the same thing to Go's os.Getenv.
//
// The /tmp/irrlicht rung (SonarQube javascript:S5443, "publicly writable
// directory") is daemonaddr.AddrFilePath's own fallback
// (core/pkg/daemonaddr/daemonaddr.go:289), taken only when HOME is empty, and
// the panel handles it as follows (read off the code below, not promised
// beyond it): the file is only ever read, never written or created;
// resolveDaemonAddress reads it only after `$.fs.stat` answers
// isReadableAddrFile (`isLink === false && kind === 'file'`); and all it takes
// from the text is a port number in 1..65535 (fixedPortOf), the host always
// being localhost (hostPortOf).
export function addrFilePath({ irrlichtHome, home }) {
  if (irrlichtHome) return cleanPath(irrlichtHome + '/' + ADDR_FILE_NAME);
  if (!home) return cleanPath('/tmp/irrlicht/' + ADDR_FILE_NAME); // NOSONAR (javascript:S5443) — see comment above
  return cleanPath(home + '/.local/share/irrlicht/' + ADDR_FILE_NAME);
}

// addrFromFileText mirrors publishedAddr's read: the first 64 bytes of the
// file, then strings.TrimSpace. Not "refuse a file over 64 bytes": Go reads
// through io.LimitReader, so a longer file whose first 64 bytes hold an
// address plus whitespace still names that address.
//
// Two differences remain, stated rather than hidden, and both need a file the
// daemon did not write (it writes one ASCII host:port line):
//   - `$.fs.read` hands the file over as decoded text, so a byte that is not
//     valid UTF-8 arrives as U+FFFD (three bytes) before the 64-byte cut
//     instead of after it;
//   - `$.fs.read` rejects a file over 4 MiB, where Go would still read its
//     first 64 bytes; the ladder then falls through to the default port.
export function addrFromFileText(text) {
  if (typeof text !== 'string') return '';
  const bytes = new TextEncoder().encode(text).slice(0, MAX_ADDR_FILE_BYTES);
  return goTrimSpace(new TextDecoder().decode(bytes));
}

// isReadableAddrFile mirrors publishedAddr's guard, `os.Lstat(path)` then
// `Mode().IsRegular()`, on what `$.fs.stat` answers: `isLink` is whether the
// path itself is a symlink (Lstat's view), `kind` what it leads to. A symlink
// to a character device such as /dev/zero would otherwise feed an unbounded
// read, which is the reason Go refuses anything but a regular file.
export function isReadableAddrFile(stat) {
  return !!stat && stat.isLink === false && stat.kind === 'file';
}

// resolveDaemonAddress walks the ladder. `io` carries the reads the hooks
// module performs through `$`, each a function resolving a value or
// undefined; any of them may reject, which counts as "not there":
//   bindAddr()      → IRRLICHT_BIND_ADDR
//   irrlichtHome()  → IRRLICHT_HOME
//   home()          → HOME
//   stat(path)      → { kind, isLink } (a `$.fs.stat` answer)
//   read(path)      → the file's text
// Resolves { port, source, addrFile } where source is 'env', 'file' or
// 'default'. The view prints only the port (hostPortOf); `source` and
// `addrFile` are returned so the tests can tell which rung of the ladder
// answered (address.test.js).
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
  if (isReadableAddrFile(await settle(io.stat, addrFile))) {
    const fromFile = fixedPortOf(addrFromFileText(await settle(io.read, addrFile)));
    if (fromFile !== null) return { port: fromFile, source: 'file', addrFile };
  }
  return { port: DEFAULT_PORT, source: 'default', addrFile };
}

// hostPortOf is what the view prints: the host this client dials and the
// resolved port.
export function hostPortOf(resolved) {
  return 'localhost:' + resolved.port;
}

// sessionsURL is the endpoint the panel polls. Plain http on purpose
// (SonarQube javascript:S5332): the daemon serves no TLS at all, only plain
// HTTP over the TCP listener in core/cmd/irrlichd (net.Listen at
// startup.go:519, srv.Serve(tcpL) at main.go:804; no crypto/tls in that
// package), bound to 127.0.0.1:7837 unless IRRLICHT_BIND_ADDR says otherwise
// (daemonaddr.go:93). The panel dials localhost whatever host that names
// (hostPortOf), so the request never leaves the machine.
export function sessionsURL(resolved) {
  return 'http://' + hostPortOf(resolved) + '/api/v1/sessions'; // NOSONAR (javascript:S5332) — see comment above
}
