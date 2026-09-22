#!/usr/bin/env bash
# Tests for site/relay.sh. Every case runs the real installer under dash with
# all system writes redirected below a temporary root and all privileged or
# host-changing commands replaced by stubs.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
INSTALL_SH="$REPO_ROOT/site/relay.sh"
FIXTURES="$DIR/testdata/relay-install"
NAME="relay-install_test"

POSIX_SH=""
for candidate in dash ash; do
    if command -v "$candidate" >/dev/null 2>&1; then
        POSIX_SH="$(command -v "$candidate")"
        break
    fi
done
INSTALL_RUNNER="${POSIX_SH:-sh}"

fails=0
pass() { printf 'PASS: %s\n' "$1"; }
fail() {
    printf 'FAIL: %s\n' "$1"
    [ $# -gt 1 ] && printf '      %s\n' "$2"
    fails=$((fails + 1))
    return 0
}
assert_eq() {
    if [[ "$1" == "$2" ]]; then pass "$3"; else fail "$3" "expected [$2] got [$1]"; fi
}
assert_contains() {
    local haystack="$1" needle="$2" what="$3"
    case "$haystack" in
        *"$needle"*) pass "$what" ;;
        *) fail "$what" "expected to find [$needle] in: $(printf '%s' "$haystack" | head -c 500)" ;;
    esac
}
assert_not_contains() {
    local haystack="$1" needle="$2" what="$3"
    case "$haystack" in
        *"$needle"*) fail "$what" "did not expect [$needle] in: $(printf '%s' "$haystack" | head -c 500)" ;;
        *) pass "$what" ;;
    esac
}
assert_present() {
    if [[ -e "$1" ]]; then pass "$2"; else fail "$2" "expected [$1] to exist"; fi
}
assert_absent() {
    if [[ -e "$1" ]]; then fail "$2" "expected [$1] to be absent"; else pass "$2"; fi
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# This is the load-bearing safety seam. Refuse before a case runs if the real
# installer no longer routes every managed path through the temporary root.
if ! grep -q 'IRRLICHT_RELAY_TEST_ROOT' "$INSTALL_SH"; then
    echo "$NAME: REFUSING TO RUN — $INSTALL_SH no longer honours IRRLICHT_RELAY_TEST_ROOT." >&2
    echo "  Without it this suite can modify /opt, /var/lib, /etc and systemd." >&2
    exit 1
fi

write_stub() {
    local path="$1"
    shift
    mkdir -p "$(dirname "$path")"
    printf '%s\n' "$@" >"$path"
    chmod +x "$path"
}

checksum_file() {
    local dir="$1"
    shift
    if command -v shasum >/dev/null 2>&1; then
        (cd "$dir" && shasum -a 256 "$@" >checksums.sha256)
    else
        (cd "$dir" && sha256sum "$@" >checksums.sha256)
    fi
}

# new_env <name> <good|flat> [bad-checksum]
new_env() {
    local name="$1" layout="$2" checksum="${3:-good}"
    local env_root="$WORK/$name"
    local release="$env_root/release" source="$env_root/source"
    local asset="irrlichtrelay-linux-amd64.tar.gz"
    mkdir -p "$env_root/root" "$env_root/bin" "$release" "$source"
    cp -R "$FIXTURES/$layout/." "$source/"
    chmod +x "$source/bin/irrlichtrelay"
    tar -czf "$release/$asset" -C "$source" .
    cp "$release/$asset" "$release/irrlichtrelay-linux-arm64.tar.gz"
    checksum_file "$release" "$asset" "irrlichtrelay-linux-arm64.tar.gz"
    if [[ "$checksum" == "bad-checksum" ]]; then
        printf '%064d  %s\n' 0 "$asset" >"$release/checksums.sha256"
    fi

    write_stub "$env_root/bin/uname" '#!/bin/sh' \
        'case "${1:-}" in' \
        '  -s) printf "%s\n" "${RELAY_TEST_UNAME_S:-Linux}" ;;' \
        '  -m) printf "%s\n" "${RELAY_TEST_UNAME_M:-x86_64}" ;;' \
        '  *) exit 64 ;;' \
        'esac'

    write_stub "$env_root/bin/curl" '#!/bin/sh' \
        'proto=0; redir=0; output=""; url=""' \
        'while [ "$#" -gt 0 ]; do' \
        '  case "$1" in' \
        '    --proto) [ "${2:-}" = "=https" ] && proto=1; shift 2 ;;' \
        '    --proto-redir) [ "${2:-}" = "=https" ] && redir=1; shift 2 ;;' \
        '    -o) output="${2:-}"; shift 2 ;;' \
        '    -w) shift 2 ;;' \
        '    -*) shift ;;' \
        '    *) url="$1"; shift ;;' \
        '  esac' \
        'done' \
        '[ "$proto" -eq 1 ] || { echo "curl stub: missing --proto =https" >&2; exit 90; }' \
        '[ "$redir" -eq 1 ] || { echo "curl stub: missing --proto-redir =https" >&2; exit 91; }' \
        'printf "curl %s\n" "$url" >>"$RELAY_TEST_LOG"' \
        'case "$url" in' \
        '  */checksums.sha256) cp "$RELAY_TEST_RELEASE/checksums.sha256" "$output" ;;' \
        '  */irrlichtrelay-linux-amd64.tar.gz) cp "$RELAY_TEST_RELEASE/irrlichtrelay-linux-amd64.tar.gz" "$output" ;;' \
        '  */irrlichtrelay-linux-arm64.tar.gz) cp "$RELAY_TEST_RELEASE/irrlichtrelay-linux-amd64.tar.gz" "$output" ;;' \
        '  */releases/latest) printf "https://github.com/ingo-eichhorst/Irrlicht/releases/tag/v0.6.4" ;;' \
        '  *) echo "curl stub: unexpected URL $url" >&2; exit 92 ;;' \
        'esac'

    write_stub "$env_root/bin/systemctl" '#!/bin/sh' \
        'printf "systemctl %s\n" "$*" >>"$RELAY_TEST_LOG"' \
        'exit 0'
    write_stub "$env_root/bin/getent" '#!/bin/sh' \
        'printf "getent %s\n" "$*" >>"$RELAY_TEST_LOG"' \
        '[ -f "$RELAY_TEST_USER_STATE" ]'
    write_stub "$env_root/bin/useradd" '#!/bin/sh' \
        'printf "useradd %s\n" "$*" >>"$RELAY_TEST_LOG"' \
        ': >"$RELAY_TEST_USER_STATE"'
    write_stub "$env_root/bin/userdel" '#!/bin/sh' \
        'printf "userdel %s\n" "$*" >>"$RELAY_TEST_LOG"' \
        'rm -f "$RELAY_TEST_USER_STATE"'
    write_stub "$env_root/bin/chown" '#!/bin/sh' \
        'printf "chown %s\n" "$*" >>"$RELAY_TEST_LOG"'
    write_stub "$env_root/bin/runuser" '#!/bin/sh' \
        'printf "runuser %s\n" "$*" >>"$RELAY_TEST_LOG"' \
        'mkdir -p "$IRRLICHT_RELAY_TEST_ROOT/var/lib/irrlichtrelay"' \
        'printf "%s\n" "fixture tokens" >"$IRRLICHT_RELAY_TEST_ROOT/var/lib/irrlichtrelay/tokens.json"' \
        'printf "%s\n" "token fixture-id issued (label \"first-daemon\", workspace \"\")"' \
        'printf "  %s\n" "fixture-secret-token"' \
        'printf "%s\n" "Store it now — it is shown only once and only its hash is kept."'
    write_stub "$env_root/bin/caddy" '#!/bin/sh' 'exit 0'
    write_stub "$env_root/bin/firewall-cmd" '#!/bin/sh' \
        'printf "FIREWALL-WAS-EDITED %s\n" "$*" >>"$RELAY_TEST_LOG"' \
        'exit 0'
    write_stub "$env_root/bin/iptables" '#!/bin/sh' \
        'printf "IPTABLES-WAS-EDITED %s\n" "$*" >>"$RELAY_TEST_LOG"' \
        'exit 0'
    write_stub "$env_root/bin/tailscale" '#!/bin/sh' \
        'printf "tailscale %s\n" "$*" >>"$RELAY_TEST_LOG"' \
        'if [ "${RELAY_TEST_TAILSCALE_HANG:-0}" = "1" ] && [ "${1:-}" = "serve" ] && [ "${2:-}" = "--bg" ]; then' \
        '  exec sleep 60' \
        'fi' \
        'if [ "${1:-}" = "status" ] && [ "${2:-}" = "--json" ]; then' \
        '  printf "%s\n" "{\"Self\":{\"DNSName\":\"relay.tailnet.ts.net.\"}}"' \
        'fi'

    : >"$env_root/log"
    printf '%s' "$env_root"
}

run_installer_file() {
    local script="$1" env_root="$2"
    shift 2
    env -i \
        PATH="$env_root/bin:/usr/bin:/bin:/usr/sbin:/sbin" \
        IRRLICHT_RELAY_TEST_ROOT="$env_root/root" \
        RELAY_TEST_LOG="$env_root/log" \
        RELAY_TEST_RELEASE="$env_root/release" \
        RELAY_TEST_BIN="${RELAY_TEST_BIN:-$env_root/bin}" \
        RELAY_TEST_USER_STATE="$env_root/user-exists" \
        RELAY_TEST_UNAME_S="${RELAY_TEST_UNAME_S:-Linux}" \
        RELAY_TEST_UNAME_M="${RELAY_TEST_UNAME_M:-x86_64}" \
        RELAY_TEST_TAILSCALE_HANG="${RELAY_TEST_TAILSCALE_HANG:-0}" \
        IRRLICHT_RELAY_TAILSCALE_TICKS="${IRRLICHT_RELAY_TAILSCALE_TICKS:-30}" \
        "$INSTALL_RUNNER" "$script" "$@" 2>&1
}

run_installer() {
    local env_root="$1"
    shift
    run_installer_file "$INSTALL_SH" "$env_root" "$@"
}

# 1. A valid archive installs the complete layout, unit and first token.
root="$(new_env success good)"
out="$(run_installer "$root" --version 0.6.4)"; rc=$?
assert_eq "$rc" "0" "install: exits 0"
assert_present "$root/root/opt/irrlichtrelay/bin/irrlichtrelay" "install: installs the relay binary"
assert_present "$root/root/opt/irrlichtrelay/Resources/web/index.html" "install: keeps the dashboard layout"
assert_present "$root/root/etc/systemd/system/irrlichtrelay.service" "install: writes the systemd unit"
if [[ -f "$root/root/etc/systemd/system/irrlichtrelay.service" ]]; then
    unit="$(cat "$root/root/etc/systemd/system/irrlichtrelay.service")"
    assert_contains "$unit" "ExecStart=/opt/irrlichtrelay/bin/irrlichtrelay serve" "install: unit uses the packaged binary"
    assert_not_contains "$unit" "--public-url" "install: loopback mode does not invent a public URL"
fi
assert_contains "$(cat "$root/log")" "IRRLICHT_HOME=/var/lib/irrlichtrelay" "install: token CLI shares the service state dir"
assert_contains "$out" "fixture-secret-token" "install: prints the first token"
assert_contains "$out" "stable HTTPS origin" "install: explains why phone pairing is unavailable"

# The existing state survives a reinstall, and the one-time secret is not
# minted or printed a second time.
out="$(run_installer "$root" --version 0.6.4)"; rc=$?
assert_eq "$rc" "0" "reinstall: exits 0"
assert_contains "$out" "Existing tokens" "reinstall: reports that existing tokens were kept"
assert_not_contains "$out" "fixture-secret-token" "reinstall: does not print a second token"
assert_eq "$(grep -c '^runuser ' "$root/log")" "1" "reinstall: token CLI ran exactly once"

# 2. A checksum mismatch refuses before it writes an install.
root="$(new_env checksum good bad-checksum)"
out="$(run_installer "$root" --version 0.6.4)"; rc=$?
[[ "$rc" -ne 0 ]] && pass "checksum: exits non-zero" || fail "checksum: exits non-zero"
assert_contains "$out" "Checksum mismatch" "checksum: names the refusal"
assert_absent "$root/root/opt/irrlichtrelay" "checksum: leaves the install path untouched"

# 3. An unsupported architecture is named. It is not allowed to fall through
# to one of the two release assets.
root="$(new_env architecture good)"
RELAY_TEST_UNAME_M=riscv64
out="$(run_installer "$root" --version 0.6.4)"; rc=$?
unset RELAY_TEST_UNAME_M
[[ "$rc" -ne 0 ]] && pass "architecture: exits non-zero" || fail "architecture: exits non-zero"
assert_contains "$out" "riscv64" "architecture: names the value it rejected"
assert_absent "$root/root/opt/irrlichtrelay" "architecture: downloads and installs nothing"

# Both published architectures are mapped explicitly.
root="$(new_env arm64 good)"
RELAY_TEST_UNAME_M=aarch64
out="$(run_installer "$root" --version 0.6.4)"; rc=$?
unset RELAY_TEST_UNAME_M
assert_eq "$rc" "0" "architecture: aarch64 installs"
assert_contains "$(cat "$root/log")" "irrlichtrelay-linux-arm64.tar.gz" "architecture: aarch64 selects the arm64 asset"

# 4. This committed mutation has a flat binary + web/ layout. It carries a
# valid checksum. The installer must reject it because it would answer 503 on
# / after installation.
root="$(new_env flat-layout flat)"
out="$(run_installer "$root" --version 0.6.4)"; rc=$?
[[ "$rc" -ne 0 ]] && pass "layout: exits non-zero" || fail "layout: exits non-zero"
assert_contains "$out" "Resources/web/index.html" "layout: names the missing dashboard path"
assert_absent "$root/root/opt/irrlichtrelay" "layout: does not install the broken archive"

# 5. Uninstall reverses the files and user created by a successful install.
root="$(new_env uninstall good)"
run_installer "$root" --version 0.6.4 >/dev/null; install_rc=$?
out="$(run_installer "$root" --uninstall)"; rc=$?
assert_eq "$install_rc" "0" "uninstall setup: install succeeds"
assert_eq "$rc" "0" "uninstall: exits 0"
assert_absent "$root/root/opt/irrlichtrelay" "uninstall: removes the install tree"
assert_absent "$root/root/var/lib/irrlichtrelay" "uninstall: removes the state tree"
assert_absent "$root/root/etc/systemd/system/irrlichtrelay.service" "uninstall: removes the unit"
assert_contains "$(cat "$root/log")" "userdel irrlichtrelay" "uninstall: removes the service user"
assert_contains "$out" "uninstalled" "uninstall: reports completion"

# 6. Domain mode writes only a Caddy reverse proxy, passes the same HTTPS
# origin to the relay and prints firewall commands without executing them.
root="$(new_env domain good)"
out="$(run_installer "$root" --version 0.6.4 --domain relay.example.com)"; rc=$?
assert_eq "$rc" "0" "domain: exits 0"
assert_present "$root/root/etc/caddy/Caddyfile" "domain: writes the Caddyfile"
if [[ -f "$root/root/etc/caddy/Caddyfile" ]]; then
    caddyfile="$(cat "$root/root/etc/caddy/Caddyfile")"
    assert_contains "$caddyfile" "relay.example.com" "domain: Caddyfile uses the requested domain"
    assert_contains "$caddyfile" "reverse_proxy 127.0.0.1:7839" "domain: Caddyfile proxies to loopback"
fi
unit="$(cat "$root/root/etc/systemd/system/irrlichtrelay.service" 2>/dev/null || true)"
assert_contains "$unit" "--public-url https://relay.example.com" "domain: unit enables QR pairing with the same origin"
assert_contains "$out" "wss://relay.example.com" "domain: prints the Mac relay URL"
assert_contains "$out" "firewall-cmd --zone=public" "domain: prints the detected firewall command"
assert_not_contains "$(cat "$root/log")" "FIREWALL-WAS-EDITED" "domain: never edits firewalld"
assert_not_contains "$(cat "$root/log")" "IPTABLES-WAS-EDITED" "domain: never edits iptables"

# A Caddy install uses only the host package manager. Its marker lets
# --uninstall remove that package when the managed Caddyfile is still intact.
root="$(new_env caddy-package good)"
rm -f "$root/bin/caddy"
write_stub "$root/bin/apt-cache" '#!/bin/sh' \
    'printf "apt-cache %s\n" "$*" >>"$RELAY_TEST_LOG"' \
    'exit 0'
write_stub "$root/bin/apt-get" '#!/bin/sh' \
    'printf "apt-get %s\n" "$*" >>"$RELAY_TEST_LOG"' \
    'if [ "${1:-}" = "install" ] || [ "${3:-}" = "install" ]; then' \
    '  printf "%s\n" "#!/bin/sh" "exit 0" >"$RELAY_TEST_BIN/caddy"' \
    '  chmod +x "$RELAY_TEST_BIN/caddy"' \
    'fi'
out="$(RELAY_TEST_BIN="$root/bin" run_installer "$root" --version 0.6.4 --domain relay.example.com)"; rc=$?
assert_eq "$rc" "0" "caddy package: domain install exits 0"
assert_contains "$(cat "$root/log")" "apt-get install -y caddy" "caddy package: uses the host package manager"
assert_present "$root/root/opt/irrlichtrelay/.caddy-package-apt" "caddy package: records ownership for uninstall"
out="$(RELAY_TEST_BIN="$root/bin" run_installer "$root" --uninstall)"; rc=$?
assert_eq "$rc" "0" "caddy package: uninstall exits 0"
assert_contains "$(cat "$root/log")" "apt-get remove -y caddy" "caddy package: uninstall reverses its package install"
assert_absent "$root/root/etc/caddy/Caddyfile" "caddy package: uninstall removes the managed Caddyfile"

# A host repository that cannot resolve Caddy causes a refusal. The output
# gives the official upstream command, but the installer does not run it.
root="$(new_env caddy-unavailable good)"
rm -f "$root/bin/caddy"
write_stub "$root/bin/apt-get" '#!/bin/sh' \
    'printf "apt-get %s\n" "$*" >>"$RELAY_TEST_LOG"'
write_stub "$root/bin/apt-cache" '#!/bin/sh' \
    'printf "apt-cache %s\n" "$*" >>"$RELAY_TEST_LOG"' \
    'exit 1'
out="$(run_installer "$root" --version 0.6.4 --domain relay.example.com)"; rc=$?
[[ "$rc" -ne 0 ]] && pass "caddy unavailable: exits non-zero" || fail "caddy unavailable: exits non-zero"
assert_contains "$out" "will not add a third-party" "caddy unavailable: states the repository boundary"
assert_contains "$out" "dl.cloudsmith.io/public/caddy/stable" "caddy unavailable: prints the official fallback"
assert_not_contains "$(cat "$root/log")" "cloudsmith" "caddy unavailable: does not run the fallback"
assert_absent "$root/root/opt/irrlichtrelay" "caddy unavailable: refuses before installing the relay"

# An existing Caddy configuration belongs to the operator. Refuse it before
# installing Caddy or changing the relay install.
root="$(new_env caddy-existing good)"
rm -f "$root/bin/caddy"
write_stub "$root/bin/apt-cache" '#!/bin/sh' \
    'printf "apt-cache %s\n" "$*" >>"$RELAY_TEST_LOG"' \
    'exit 0'
write_stub "$root/bin/apt-get" '#!/bin/sh' \
    'printf "apt-get %s\n" "$*" >>"$RELAY_TEST_LOG"'
mkdir -p "$root/root/etc/caddy"
printf '%s\n' 'existing.example.com { respond "keep me" }' >"$root/root/etc/caddy/Caddyfile"
out="$(run_installer "$root" --version 0.6.4 --domain relay.example.com)"; rc=$?
[[ "$rc" -ne 0 ]] && pass "caddy existing: exits non-zero" || fail "caddy existing: exits non-zero"
assert_contains "$out" "Refusing to replace" "caddy existing: explains the refusal"
assert_contains "$(cat "$root/root/etc/caddy/Caddyfile")" "keep me" "caddy existing: preserves the operator config"
assert_not_contains "$(cat "$root/log")" "apt-get" "caddy existing: refuses before installing a package"
assert_absent "$root/root/opt/irrlichtrelay" "caddy existing: refuses before installing the relay"

# 7. Tailscale mode derives the HTTPS origin and configures Serve.
root="$(new_env tailscale good)"
out="$(run_installer "$root" --version 0.6.4 --tailscale)"; rc=$?
assert_eq "$rc" "0" "tailscale: exits 0"
unit="$(cat "$root/root/etc/systemd/system/irrlichtrelay.service" 2>/dev/null || true)"
assert_contains "$unit" "--public-url https://relay.tailnet.ts.net" "tailscale: unit uses the derived origin"
assert_contains "$(cat "$root/log")" "tailscale serve --bg 7839" "tailscale: configures HTTPS forwarding"
assert_contains "$out" "wss://relay.tailnet.ts.net" "tailscale: prints the Mac relay URL"
out="$(run_installer "$root" --uninstall)"; rc=$?
assert_eq "$rc" "0" "tailscale: uninstall exits 0"
assert_contains "$(cat "$root/log")" "tailscale serve --https=443 off" "tailscale: uninstall removes the Serve rule"

# Tailscale can wait for an operator to enable Serve. The installer observes
# the command and fails on a deadline instead of hanging.
root="$(new_env tailscale-hang good)"
started="$(date +%s)"
RELAY_TEST_TAILSCALE_HANG=1
IRRLICHT_RELAY_TAILSCALE_TICKS=1
out="$(run_installer "$root" --version 0.6.4 --tailscale)"; rc=$?
unset RELAY_TEST_TAILSCALE_HANG IRRLICHT_RELAY_TAILSCALE_TICKS
elapsed=$(( $(date +%s) - started ))
[[ "$rc" -ne 0 ]] && pass "tailscale wait: exits non-zero" || fail "tailscale wait: exits non-zero"
assert_contains "$out" "did not finish within 1s" "tailscale wait: names the expired deadline"
# A loaded host can delay the shell after the installer's internal deadline.
# The outer evidence stays below the stub's 60-second hang while allowing that
# scheduling delay.
if [[ "$elapsed" -le 30 ]]; then pass "tailscale wait: is bounded (${elapsed}s)"; else fail "tailscale wait: is bounded" "took ${elapsed}s"; fi

# 8. The two TLS modes are exclusive.
root="$(new_env exclusive good)"
out="$(run_installer "$root" --version 0.6.4 --domain relay.example.com --tailscale)"; rc=$?
[[ "$rc" -ne 0 ]] && pass "options: conflicting TLS modes exit non-zero" || fail "options: conflicting TLS modes exit non-zero"
assert_contains "$out" "cannot be used together" "options: names the conflict"

# 9. Linux with systemd is an explicit platform boundary.
root="$(new_env os good)"
RELAY_TEST_UNAME_S=Darwin
out="$(run_installer "$root" --version 0.6.4)"; rc=$?
unset RELAY_TEST_UNAME_S
[[ "$rc" -ne 0 ]] && pass "platform: non-Linux exits non-zero" || fail "platform: non-Linux exits non-zero"
assert_contains "$out" "Linux" "platform: names the supported OS"

# 10. Mutation proof for the HTTPS redirect guard. The curl stub simulates a
# downloader that refuses to proceed unless both protocol pins are present.
# Removing --proto-redir from a copy of the installer must make the run red.
mutant="$WORK/relay-without-proto-redir.sh"
sed "s/--proto-redir '=https'//" "$INSTALL_SH" >"$mutant"
root="$(new_env protocol-mutation good)"
out="$(run_installer_file "$mutant" "$root" --version 0.6.4)"; rc=$?
[[ "$rc" -ne 0 ]] && pass "protocol mutation: missing redirect pin is caught" || fail "protocol mutation: missing redirect pin is caught"
assert_contains "$out" "missing --proto-redir" "protocol mutation: refusal identifies the missing guard"

printf '\n%s: runner=%s\n' "$NAME" "$INSTALL_RUNNER"
if [[ "$fails" -ne 0 ]]; then
    printf '%s: %d FAILED\n' "$NAME" "$fails"
    exit 1
fi
printf '%s: ALL PASS\n' "$NAME"
