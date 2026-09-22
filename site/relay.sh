#!/bin/sh
# Irrlicht relay installer — https://irrlicht.io
#
# Usage:
#   curl -fsSL https://irrlicht.io/relay.sh | sh -s -- --domain relay.example.com
#   curl -fsSL https://irrlicht.io/relay.sh | sh -s -- --tailscale
#   curl -fsSL https://irrlicht.io/relay.sh | sh -s -- --uninstall

set -eu

REPO="ingo-eichhorst/Irrlicht"
VERSION=""
DOMAIN=""
TAILSCALE=0
UNINSTALL=0

# Every managed filesystem path passes through this root. Production uses /.
# The override lets tools/lib/relay-install_test.sh run the real installer
# without reaching /opt, /var/lib or /etc on the test host.
IRRLICHT_RELAY_TEST_ROOT="${IRRLICHT_RELAY_TEST_ROOT:-/}"
case "$IRRLICHT_RELAY_TEST_ROOT" in
    /*) ;;
    *) printf '%s\n' "irrlichtrelay installer: IRRLICHT_RELAY_TEST_ROOT must be an absolute path" >&2; exit 1 ;;
esac

root_path() {
    if [ "$IRRLICHT_RELAY_TEST_ROOT" = "/" ]; then
        printf '/%s' "${1#/}"
    else
        printf '%s/%s' "${IRRLICHT_RELAY_TEST_ROOT%/}" "${1#/}"
    fi
}

INSTALL_DIR=$(root_path /opt/irrlichtrelay)
STATE_DIR=$(root_path /var/lib/irrlichtrelay)
UNIT_PATH=$(root_path /etc/systemd/system/irrlichtrelay.service)
CADDYFILE=$(root_path /etc/caddy/Caddyfile)
CADDY_BACKUP="${CADDYFILE}.pre-irrlichtrelay"

if [ -t 1 ]; then
    BOLD=$(printf '\033[1m')
    DIM=$(printf '\033[2m')
    GREEN=$(printf '\033[32m')
    RED=$(printf '\033[31m')
    YELLOW=$(printf '\033[33m')
    RESET=$(printf '\033[0m')
else
    BOLD="" DIM="" GREEN="" RED="" YELLOW="" RESET=""
fi

say()  { printf '%s\n' "$*"; }
step() { printf '  %s…%s ' "$*" "$DIM"; }
ok()   { printf '%s✓%s\n' "$GREEN" "$RESET"; }
fail() { printf '%s✗%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }
warn() { printf '%s!%s %s\n' "$YELLOW" "$RESET" "$*" >&2; }

# GitHub release assets redirect to a CDN. Pin the first request and every
# redirect to HTTPS so a redirect cannot downgrade a verified download.
fetch() { curl -fsSL --proto '=https' --proto-redir '=https' "$@"; }

usage() {
    cat <<'EOF'
Irrlicht relay installer

Usage:
  curl -fsSL https://irrlicht.io/relay.sh | sh -s -- [options]

Options:
  --domain DOMAIN      Use Caddy for HTTPS at DOMAIN
  --tailscale          Use Tailscale Serve for HTTPS
  --version VERSION    Install a specific version (default: latest)
  --uninstall          Remove the relay install and its state
  -h, --help           Show this help

Without --domain or --tailscale, the relay starts on loopback. Phone pairing
needs one of the HTTPS modes because it needs a stable HTTPS origin.
EOF
}

run_root() {
    if [ "$IRRLICHT_RELAY_TEST_ROOT" != "/" ] || [ "$(id -u)" -eq 0 ]; then
        "$@"
    else
        sudo "$@"
    fi
}

run_as_relay() {
    if [ "$IRRLICHT_RELAY_TEST_ROOT" != "/" ] || [ "$(id -u)" -eq 0 ]; then
        command -v runuser >/dev/null 2>&1 || fail "runuser is required to issue the relay token as irrlichtrelay."
        runuser -u irrlichtrelay -- env IRRLICHT_HOME=/var/lib/irrlichtrelay "$@"
    else
        sudo -u irrlichtrelay env IRRLICHT_HOME=/var/lib/irrlichtrelay "$@"
    fi
}

sha256_verify() {
    _verify_dir="$1"
    _verify_asset="$2"
    _verify_matches=$(grep -c " $_verify_asset\$" "$_verify_dir/checksums.sha256") || true
    [ "$_verify_matches" -eq 1 ] || return 1
    if command -v shasum >/dev/null 2>&1; then
        (cd "$_verify_dir" && grep " $_verify_asset\$" checksums.sha256 | shasum -a 256 -c --status)
    elif command -v sha256sum >/dev/null 2>&1; then
        (cd "$_verify_dir" && grep " $_verify_asset\$" checksums.sha256 | sha256sum -c --status)
    else
        fail "Need sha256sum or shasum to verify the download."
    fi
}

validate_domain() {
    printf '%s\n' "$1" | grep -Eq '^([A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?\.)+[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$' \
        || fail "Invalid domain: $1 (give a hostname such as relay.example.com)."
}

read_tailscale_origin() {
    _tailscale_json=$(tailscale status --json) || fail "Could not read tailscale status."
    _tailscale_name=$(printf '%s\n' "$_tailscale_json" \
        | sed -n 's/.*"DNSName"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
        | sed -n '1p')
    _tailscale_name=${_tailscale_name%.}
    case "$_tailscale_name" in
        ""|*[!A-Za-z0-9.-]*) fail "tailscale status returned no valid Self.DNSName." ;;
    esac
    printf 'https://%s' "$_tailscale_name"
}

caddy_package_unavailable() {
    warn "The host package manager has no caddy package."
    warn "The installer will not add a third-party package repository."
    if command -v apt-get >/dev/null 2>&1; then
        warn "Official Caddy install commands:"
        warn "  sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https curl"
        warn "  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg"
        warn "  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list"
        warn "  sudo chmod o+r /usr/share/keyrings/caddy-stable-archive-keyring.gpg /etc/apt/sources.list.d/caddy-stable.list"
        warn "  sudo apt update && sudo apt install caddy"
    elif command -v dnf >/dev/null 2>&1 || command -v yum >/dev/null 2>&1; then
        warn "Official Caddy install commands:"
        if [ -f "$(root_path /etc/fedora-release)" ]; then
            warn "  sudo dnf install dnf5-plugins"
        else
            warn "  sudo dnf install dnf-plugins-core"
        fi
        warn "  sudo dnf copr enable @caddy/caddy"
        warn "  sudo dnf install caddy"
    else
        warn "Use the official instructions at https://caddyserver.com/docs/install."
    fi
    fail "Cannot use --domain until the caddy command is available."
}

ensure_caddy() {
    if command -v caddy >/dev/null 2>&1; then
        return 0
    fi

    step "Installing Caddy from the host package manager"
    if command -v apt-get >/dev/null 2>&1 && command -v apt-cache >/dev/null 2>&1; then
        run_root apt-get update >/dev/null
        apt-cache show caddy >/dev/null 2>&1 || caddy_package_unavailable
        run_root env DEBIAN_FRONTEND=noninteractive apt-get install -y caddy >/dev/null
        CADDY_PACKAGE_MANAGER="apt"
    elif command -v dnf >/dev/null 2>&1; then
        dnf -q list --available caddy >/dev/null 2>&1 || caddy_package_unavailable
        run_root dnf install -y caddy >/dev/null
        CADDY_PACKAGE_MANAGER="dnf"
    elif command -v yum >/dev/null 2>&1; then
        yum -q list available caddy >/dev/null 2>&1 || caddy_package_unavailable
        run_root yum install -y caddy >/dev/null
        CADDY_PACKAGE_MANAGER="yum"
    elif command -v zypper >/dev/null 2>&1; then
        zypper --non-interactive search -x caddy >/dev/null 2>&1 || caddy_package_unavailable
        run_root zypper --non-interactive install caddy >/dev/null
        CADDY_PACKAGE_MANAGER="zypper"
    elif command -v pacman >/dev/null 2>&1; then
        pacman -Si caddy >/dev/null 2>&1 || caddy_package_unavailable
        run_root pacman -S --noconfirm caddy >/dev/null
        CADDY_PACKAGE_MANAGER="pacman"
    else
        caddy_package_unavailable
    fi
    command -v caddy >/dev/null 2>&1 || fail "The package manager completed but the caddy command is unavailable."
    ok
}

remove_managed_caddy_config() {
    if [ -f "$CADDYFILE" ] && grep -q '^# Managed by https://irrlicht.io/relay.sh$' "$CADDYFILE"; then
        run_root rm -f "$CADDYFILE"
        if [ -e "$CADDY_BACKUP" ]; then
            run_root mv "$CADDY_BACKUP" "$CADDYFILE"
            run_root systemctl reload caddy.service 2>/dev/null || true
        else
            run_root systemctl disable --now caddy.service 2>/dev/null || true
        fi
    fi
}

remove_caddy_package() {
    _remove_package_manager="$1"
    case "$_remove_package_manager" in
        apt) run_root apt-get remove -y caddy >/dev/null ;;
        dnf) run_root dnf remove -y caddy >/dev/null ;;
        yum) run_root yum remove -y caddy >/dev/null ;;
        zypper) run_root zypper --non-interactive remove caddy >/dev/null ;;
        pacman) run_root pacman -R --noconfirm caddy >/dev/null ;;
        *) return 1 ;;
    esac
}

write_caddy_config() {
    _caddy_temp="$TMPDIR/Caddyfile"
    cat >"$_caddy_temp" <<EOF
# Managed by https://irrlicht.io/relay.sh
$DOMAIN {
    reverse_proxy 127.0.0.1:7839
}
EOF
    if [ -e "$CADDYFILE" ] && ! grep -q '^# Managed by https://irrlicht.io/relay.sh$' "$CADDYFILE"; then
        [ ! -e "$CADDY_BACKUP" ] || fail "Refusing to replace $CADDYFILE because $CADDY_BACKUP already exists."
        run_root mv "$CADDYFILE" "$CADDY_BACKUP"
    fi
    run_root install -d -m 755 "$(dirname "$CADDYFILE")"
    run_root install -m 644 "$_caddy_temp" "$CADDYFILE"
    run_root systemctl enable --now caddy.service >/dev/null
    run_root systemctl reload caddy.service >/dev/null
}

print_firewall_help() {
    say ""
    say "  The installer did not change the firewall."
    if command -v firewall-cmd >/dev/null 2>&1; then
        say "  If this host uses firewalld, run:"
        say "    sudo firewall-cmd --zone=public --permanent --add-port=443/tcp && sudo firewall-cmd --reload"
    elif command -v iptables >/dev/null 2>&1; then
        say "  If this host uses raw iptables, insert the rule before any REJECT:"
        say "    sudo iptables -I INPUT 6 -p tcp --dport 443 -j ACCEPT"
        say "    sudo netfilter-persistent save"
    else
        say "  Permit inbound TCP port 443 in the host firewall and the cloud network firewall."
    fi
}

configure_tailscale_serve() {
    _tailscale_log="$TMPDIR/tailscale-serve.log"
    _tailscale_ticks="${IRRLICHT_RELAY_TAILSCALE_TICKS:-30}"
    case "$_tailscale_ticks" in
        ""|*[!0-9]*) fail "IRRLICHT_RELAY_TAILSCALE_TICKS must be a whole number." ;;
    esac
    if [ "$IRRLICHT_RELAY_TEST_ROOT" != "/" ] || [ "$(id -u)" -eq 0 ]; then
        tailscale serve --bg 7839 >"$_tailscale_log" 2>&1 &
    else
        sudo tailscale serve --bg 7839 >"$_tailscale_log" 2>&1 &
    fi
    _tailscale_pid=$!
    _tailscale_waited=0
    while kill -0 "$_tailscale_pid" 2>/dev/null; do
        if [ "$_tailscale_waited" -ge "$_tailscale_ticks" ]; then
            kill "$_tailscale_pid" 2>/dev/null || true
            wait "$_tailscale_pid" 2>/dev/null || true
            [ ! -s "$_tailscale_log" ] || cat "$_tailscale_log" >&2
            fail "tailscale serve did not finish within ${_tailscale_ticks}s. Enable Serve for this tailnet and run the installer again."
        fi
        sleep 1
        _tailscale_waited=$((_tailscale_waited + 1))
    done
    if wait "$_tailscale_pid"; then
        [ ! -s "$_tailscale_log" ] || cat "$_tailscale_log"
        return 0
    else
        _tailscale_status=$?
    fi
    [ ! -s "$_tailscale_log" ] || cat "$_tailscale_log" >&2
    fail "tailscale serve failed with exit $_tailscale_status. Enable Serve for this tailnet and run the installer again."
}

uninstall_relay() {
    _remove_user=0
    _old_mode=""
    _caddy_package_manager=""
    _caddy_config_owned=0
    [ -f "$INSTALL_DIR/.user-created" ] && _remove_user=1
    [ -f "$INSTALL_DIR/.mode-domain" ] && _old_mode="domain"
    [ -f "$INSTALL_DIR/.mode-tailscale" ] && _old_mode="tailscale"
    for _package_manager in apt dnf yum zypper pacman; do
        [ -f "$INSTALL_DIR/.caddy-package-$_package_manager" ] \
            && _caddy_package_manager="$_package_manager"
    done

    step "Stopping irrlichtrelay"
    run_root systemctl disable --now irrlichtrelay.service 2>/dev/null || true
    ok

    if [ "$_old_mode" = "domain" ]; then
        if [ -f "$CADDYFILE" ] && grep -q '^# Managed by https://irrlicht.io/relay.sh$' "$CADDYFILE"; then
            _caddy_config_owned=1
        fi
        if [ "$_caddy_config_owned" -eq 1 ]; then
            step "Removing the Caddy relay config"
            remove_managed_caddy_config
            ok
        else
            warn "The Caddyfile is no longer managed by relay.sh. The installer kept it and the Caddy package."
        fi
        if [ -n "$_caddy_package_manager" ] && [ "$_caddy_config_owned" -eq 1 ]; then
            step "Removing the Caddy package installed for the relay"
            if remove_caddy_package "$_caddy_package_manager"; then
                ok
            else
                printf '\n'
                warn "Could not remove the Caddy package. Remove it with $_caddy_package_manager."
            fi
        fi
    elif [ "$_old_mode" = "tailscale" ] && command -v tailscale >/dev/null 2>&1; then
        step "Removing Tailscale Serve"
        run_root tailscale serve --https=443 off >/dev/null 2>&1 || warn "Could not remove the Tailscale Serve rule."
        ok
    fi

    step "Removing relay files and state"
    case "$INSTALL_DIR:$STATE_DIR:$UNIT_PATH" in
        */opt/irrlichtrelay:*/var/lib/irrlichtrelay:*/etc/systemd/system/irrlichtrelay.service) ;;
        *) fail "Internal path guard refused uninstall." ;;
    esac
    run_root rm -rf "$INSTALL_DIR" "$STATE_DIR"
    run_root rm -f "$UNIT_PATH"
    run_root systemctl daemon-reload >/dev/null 2>&1 || true
    ok

    if [ "$_remove_user" -eq 1 ]; then
        step "Removing the irrlichtrelay system user"
        run_root userdel irrlichtrelay 2>/dev/null || true
        ok
    fi

    say ""
    say "  ${GREEN}✓${RESET} Irrlicht relay uninstalled"
    say ""
}

while [ $# -gt 0 ]; do
    case "$1" in
        --domain)
            [ $# -ge 2 ] || fail "--domain needs a hostname."
            DOMAIN="$2"
            shift 2
            ;;
        --domain=*) DOMAIN="${1#*=}"; shift ;;
        --tailscale) TAILSCALE=1; shift ;;
        --version)
            [ $# -ge 2 ] || fail "--version needs a value."
            VERSION="$2"
            shift 2
            ;;
        --version=*) VERSION="${1#*=}"; shift ;;
        --uninstall) UNINSTALL=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) fail "Unknown option: $1 (try --help)" ;;
    esac
done

[ -z "$DOMAIN" ] || validate_domain "$DOMAIN"
if [ -n "$DOMAIN" ] && [ "$TAILSCALE" -eq 1 ]; then
    fail "--domain and --tailscale cannot be used together."
fi

[ "$(uname -s)" = "Linux" ] || fail "Unsupported operating system: $(uname -s). This installer supports Linux with systemd."
command -v systemctl >/dev/null 2>&1 || fail "systemctl is required. This installer supports Linux with systemd."

if [ "$IRRLICHT_RELAY_TEST_ROOT" = "/" ] && [ "$(id -u)" -ne 0 ]; then
    command -v sudo >/dev/null 2>&1 || fail "Run as root, or install sudo and run this command again."
    sudo -v
fi

if [ "$UNINSTALL" -eq 1 ]; then
    uninstall_relay
    exit 0
fi

for _required_command in curl tar grep sed install useradd chown; do
    command -v "$_required_command" >/dev/null 2>&1 || fail "$_required_command is required but not found."
done

case "$(uname -m)" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) fail "Unsupported Linux architecture: $(uname -m). Supported architectures: x86_64 and aarch64." ;;
esac

PUBLIC_URL=""
PUBLIC_HOST=""
MODE="loopback"
if [ -n "$DOMAIN" ]; then
    PUBLIC_URL="https://$DOMAIN"
    PUBLIC_HOST="$DOMAIN"
    MODE="domain"
elif [ "$TAILSCALE" -eq 1 ]; then
    command -v tailscale >/dev/null 2>&1 || fail "--tailscale requires the tailscale command."
    PUBLIC_URL=$(read_tailscale_origin)
    PUBLIC_HOST=${PUBLIC_URL#https://}
    MODE="tailscale"
fi

say ""
say "  ${BOLD}Irrlicht relay installer${RESET}"
say ""

if [ -z "$VERSION" ]; then
    step "Detecting latest version"
    VERSION=$(fetch -o /dev/null -w '%{url_effective}' \
        "https://github.com/$REPO/releases/latest" \
        | grep -oE '[0-9]+\.[0-9]+\.[0-9]+$')
    [ -n "$VERSION" ] || fail "Could not detect the latest version."
    printf 'v%s\n' "$VERSION"
fi

case "$VERSION" in
    *[!0-9A-Za-z.+-]*|"") fail "Invalid version: $VERSION" ;;
esac

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT INT TERM

ASSET="irrlichtrelay-linux-${ARCH}.tar.gz"
BASE="https://github.com/$REPO/releases/download/v${VERSION}"

step "Downloading checksums"
fetch -o "$TMPDIR/checksums.sha256" "$BASE/checksums.sha256" \
    || fail "Could not download $BASE/checksums.sha256"
ok

step "Downloading $ASSET"
fetch -o "$TMPDIR/$ASSET" "$BASE/$ASSET" || fail "Could not download $ASSET"
ok

step "Verifying checksum"
sha256_verify "$TMPDIR" "$ASSET" || fail "Checksum mismatch — refusing to install."
ok

step "Checking the archive layout"
mkdir -p "$TMPDIR/extract"
tar -xzf "$TMPDIR/$ASSET" -C "$TMPDIR/extract" || fail "Could not extract $ASSET."
[ -x "$TMPDIR/extract/bin/irrlichtrelay" ] \
    || fail "Archive is missing executable bin/irrlichtrelay — refusing to install."
[ -f "$TMPDIR/extract/Resources/web/index.html" ] \
    || fail "Archive is missing Resources/web/index.html — refusing to install a relay whose dashboard answers 503."
ok

OLD_MODE=""
USER_CREATED=0
CADDY_PACKAGE_MANAGER=""
[ -f "$INSTALL_DIR/.mode-domain" ] && OLD_MODE="domain"
[ -f "$INSTALL_DIR/.mode-tailscale" ] && OLD_MODE="tailscale"
[ -f "$INSTALL_DIR/.user-created" ] && USER_CREATED=1
for _package_manager in apt dnf yum zypper pacman; do
    [ -f "$INSTALL_DIR/.caddy-package-$_package_manager" ] \
        && CADDY_PACKAGE_MANAGER="$_package_manager"
done

if [ "$MODE" = "domain" ]; then
    if [ -e "$CADDYFILE" ] \
        && ! grep -q '^# Managed by https://irrlicht.io/relay.sh$' "$CADDYFILE"; then
        fail "Refusing to replace the existing $CADDYFILE. Add a reverse_proxy block manually or use --tailscale."
    fi
    ensure_caddy
    if [ -e "$CADDYFILE" ] \
        && ! grep -q '^# Managed by https://irrlicht.io/relay.sh$' "$CADDYFILE" \
        && [ -e "$CADDY_BACKUP" ]; then
        fail "Refusing to replace $CADDYFILE because $CADDY_BACKUP already exists."
    fi
fi

step "Creating the service account and state directory"
if ! getent passwd irrlichtrelay >/dev/null 2>&1; then
    run_root useradd --system --no-create-home --home-dir /var/lib/irrlichtrelay --shell /usr/sbin/nologin irrlichtrelay
    USER_CREATED=1
fi
run_root install -d -m 700 "$STATE_DIR"
run_root chown irrlichtrelay:irrlichtrelay "$STATE_DIR"
ok

step "Installing to /opt/irrlichtrelay"
case "$INSTALL_DIR" in
    */opt/irrlichtrelay) ;;
    *) fail "Internal path guard refused installation." ;;
esac
run_root rm -rf "$INSTALL_DIR"
run_root install -d -m 755 "$INSTALL_DIR"
run_root cp -R "$TMPDIR/extract/." "$INSTALL_DIR/"
run_root chmod 755 "$INSTALL_DIR/bin/irrlichtrelay"
[ "$USER_CREATED" -eq 1 ] && run_root touch "$INSTALL_DIR/.user-created"
[ -n "$CADDY_PACKAGE_MANAGER" ] && run_root touch "$INSTALL_DIR/.caddy-package-$CADDY_PACKAGE_MANAGER"
run_root touch "$INSTALL_DIR/.mode-$MODE"
ok

if [ "$OLD_MODE" = "domain" ] && [ "$MODE" != "domain" ]; then
    _old_caddy_config_owned=0
    if [ -f "$CADDYFILE" ] && grep -q '^# Managed by https://irrlicht.io/relay.sh$' "$CADDYFILE"; then
        _old_caddy_config_owned=1
        remove_managed_caddy_config
    fi
    if [ -n "$CADDY_PACKAGE_MANAGER" ] && [ "$_old_caddy_config_owned" -eq 1 ]; then
        if remove_caddy_package "$CADDY_PACKAGE_MANAGER"; then
            run_root rm -f "$INSTALL_DIR/.caddy-package-$CADDY_PACKAGE_MANAGER"
            CADDY_PACKAGE_MANAGER=""
        else
            warn "Could not remove the Caddy package during the mode change."
        fi
    fi
elif [ "$OLD_MODE" = "tailscale" ] && [ "$MODE" != "tailscale" ] && command -v tailscale >/dev/null 2>&1; then
    run_root tailscale serve --https=443 off >/dev/null 2>&1 || warn "Could not remove the old Tailscale Serve rule."
fi

PUBLIC_ARGS=""
if [ -n "$PUBLIC_URL" ]; then
    PUBLIC_ARGS=" --public-url $PUBLIC_URL --origin-allowlist $PUBLIC_HOST"
fi

cat >"$TMPDIR/irrlichtrelay.service" <<EOF
[Unit]
Description=Irrlicht relay — cross-host session hub
Documentation=https://github.com/ingo-eichhorst/Irrlicht/blob/main/examples/relay/DEPLOY.md
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=irrlichtrelay
Group=irrlichtrelay
StateDirectory=irrlichtrelay
StateDirectoryMode=0700
Environment=IRRLICHT_HOME=/var/lib/irrlichtrelay
ExecStart=/opt/irrlichtrelay/bin/irrlichtrelay serve --addr 127.0.0.1:7839 --auth tokens-file${PUBLIC_ARGS}
Restart=on-failure
RestartSec=2
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes

[Install]
WantedBy=multi-user.target
EOF

step "Installing the systemd service"
run_root install -d -m 755 "$(dirname "$UNIT_PATH")"
run_root install -m 644 "$TMPDIR/irrlichtrelay.service" "$UNIT_PATH"
run_root systemctl daemon-reload >/dev/null
ok

TOKEN=""
if [ ! -f "$STATE_DIR/tokens.json" ]; then
    step "Issuing the first bearer token"
    TOKEN_OUTPUT=$(run_as_relay /opt/irrlichtrelay/bin/irrlichtrelay token issue --label first-daemon) \
        || fail "Could not issue the first bearer token."
    # core/cmd/irrlichtrelay/main.go's runTokenIssue prints the secret on the
    # line after its "token ... issued" headline, then prints a storage warning.
    TOKEN=$(printf '%s\n' "$TOKEN_OUTPUT" \
        | sed -n '/^token [^ ]* issued /{n;s/^[[:space:]]*//;p;q;}')
    case "$TOKEN" in
        ""|*[!A-Za-z0-9_-]*) fail "The token command returned an invalid bearer token." ;;
    esac
    ok
fi

if [ "$MODE" = "domain" ]; then
    step "Configuring Caddy"
    write_caddy_config
    ok
fi

step "Starting irrlichtrelay"
run_root systemctl enable --now irrlichtrelay.service >/dev/null
ok

if [ "$MODE" = "tailscale" ]; then
    step "Configuring Tailscale Serve"
    configure_tailscale_serve
    ok
fi

say ""
say "  ${GREEN}✓${RESET} ${BOLD}irrlichtrelay v$VERSION${RESET} installed"
say ""
if [ -n "$PUBLIC_URL" ]; then
    say "  Relay URL for the Mac: ${BOLD}wss://${PUBLIC_HOST}${RESET}"
else
    say "  Local relay URL: ${BOLD}ws://127.0.0.1:7839${RESET}"
    say ""
    warn "Phone pairing needs a stable HTTPS origin. Run this installer again with --domain or --tailscale."
fi
if [ -n "$TOKEN" ]; then
    say "  Bearer token (shown once): ${BOLD}$TOKEN${RESET}"
else
    say "  Existing tokens in /var/lib/irrlichtrelay/tokens.json were kept."
fi
say ""
say "  Service status: systemctl status irrlichtrelay"

if [ "$MODE" = "domain" ]; then
    print_firewall_help
fi
say ""
