#!/usr/bin/env bash
# Creates a Proxmox VE LXC container running BLAVINGAD as a systemd service.
# The agent runs as root inside the container (apt, systemctl, Docker…).
#
# Run it on the PVE host, as root:
#
#   # from a checkout of the repo: uses it as source and ./.env as config
#   ./deploy/proxmox/create-lxc.sh
#
#   # standalone: clone the code inside the container
#   REPO=https://github.com/blavingadtheplushie/core.git GIT_TOKEN=github_pat_… \
#   ENV_FILE=/root/blavingad.env ./create-lxc.sh
#
# Everything is configurable through environment variables:
#   CTID (next free id)      HOSTNAME_CT (blavingad)   CORES (4)  MEMORY_MB (4096)
#   SWAP_MB (1024)           DISK_GB (32)              STORAGE (local-lvm)
#   TEMPLATE_STORAGE (local) BRIDGE (vmbr0)            VLAN ()
#   IP (dhcp | 192.168.1.50/24)  GW ()                 DNS ()
#   UNPRIVILEGED (1)         DOCKER (1: Docker engine inside, for the agent's deploys)
#   ENV_FILE (<repo>/.env)   SRC_DIR (the repo this script lives in)
#   REPO / BRANCH (main) / GIT_TOKEN   clone instead of pushing SRC_DIR
#   SSH_KEY (path to a public key for root)   PASSWORD (root password)
#   TEMPLATE (force a template, e.g. debian-13-standard_13.6-1_amd64.tar.zst)
#   DATA_TGZ (tarball of an existing /data to migrate: DB, memories…)
#   TUNNEL (1: install cloudflared)   CF_TUNNEL_TOKEN (connector token: starts the
#     tunnel right away; defaults to a literal CLOUDFLARE_TUNNEL_TOKEN of the env file.
#     Without one, ask the agent "set up the Cloudflare tunnel" once it runs.)
set -euo pipefail

say()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mxx\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run as root on the Proxmox host"
command -v pct >/dev/null || die "pct not found: run this on a Proxmox VE host"

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/../.." 2>/dev/null && pwd || true)

CTID=${CTID:-$(pvesh get /cluster/nextid)}
HOSTNAME_CT=${HOSTNAME_CT:-blavingad}
CORES=${CORES:-4}
MEMORY_MB=${MEMORY_MB:-4096}
SWAP_MB=${SWAP_MB:-1024}
DISK_GB=${DISK_GB:-32}
STORAGE=${STORAGE:-local-lvm}
TEMPLATE_STORAGE=${TEMPLATE_STORAGE:-local}
BRIDGE=${BRIDGE:-vmbr0}
VLAN=${VLAN:-}
IP=${IP:-dhcp}
GW=${GW:-}
DNS=${DNS:-}
UNPRIVILEGED=${UNPRIVILEGED:-1}
DOCKER=${DOCKER:-1}
REPO=${REPO:-}
BRANCH=${BRANCH:-main}
GIT_TOKEN=${GIT_TOKEN:-}
SSH_KEY=${SSH_KEY:-}
PASSWORD=${PASSWORD:-}
DATA_TGZ=${DATA_TGZ:-}
TUNNEL=${TUNNEL:-1}
CF_TUNNEL_TOKEN=${CF_TUNNEL_TOKEN:-}

if [ -z "$REPO" ]; then
  SRC_DIR=${SRC_DIR:-$REPO_ROOT}
  [ -f "$SRC_DIR/go.mod" ] && [ -d "$SRC_DIR/cmd/blavingad" ] ||
    die "no source found in $SRC_DIR: run the script from the repo, or set REPO=<git url>"
  ENV_FILE=${ENV_FILE:-$SRC_DIR/.env}
fi
ENV_FILE=${ENV_FILE:-}
if [ -z "$ENV_FILE" ] || [ ! -f "$ENV_FILE" ]; then
  warn "no env file (ENV_FILE=${ENV_FILE:-unset}): the container gets .env.example, edit /etc/blavingad/blavingad.env then restart"
  ENV_FILE=""
fi
[ -z "$DATA_TGZ" ] || [ -f "$DATA_TGZ" ] || die "DATA_TGZ not found: $DATA_TGZ"
[ -z "$SSH_KEY" ] || [ -f "$SSH_KEY" ] || die "SSH_KEY not found: $SSH_KEY"
if pct status "$CTID" >/dev/null 2>&1; then die "container $CTID already exists (set CTID=…)"; fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# ---------- template ----------
say "Looking for a Debian template"
pveam update >/dev/null 2>&1 || warn "pveam update failed, using the cached list"
# Templates exist for several architectures: only take the host's.
HOST_ARCH=$(dpkg --print-architecture)
pick() { pveam available --section system | awk '{print $2}' | grep -E "^$1_.*_${HOST_ARCH}\.tar" | sort -V | tail -1 || true; }
TEMPLATE=${TEMPLATE:-$(pick debian-13-standard)}
[ -n "$TEMPLATE" ] || TEMPLATE=$(pick debian-12-standard)
[ -n "$TEMPLATE" ] || die "no Debian template for $HOST_ARCH (pveam available --section system)"
case "$TEMPLATE" in *"_${HOST_ARCH}."*) ;; *) die "template $TEMPLATE is not for this host ($HOST_ARCH)" ;; esac
if ! pveam list "$TEMPLATE_STORAGE" | grep -q "$TEMPLATE"; then
  say "Downloading $TEMPLATE"
  pveam download "$TEMPLATE_STORAGE" "$TEMPLATE"
fi

# ---------- container ----------
NET0="name=eth0,bridge=$BRIDGE,ip=$IP"
[ -n "$GW" ] && NET0+=",gw=$GW"
[ -n "$VLAN" ] && NET0+=",tag=$VLAN"
FEATURES="nesting=1"
[ "$UNPRIVILEGED" = 1 ] && FEATURES+=",keyctl=1"

ARGS=(--hostname "$HOSTNAME_CT" --cores "$CORES" --memory "$MEMORY_MB" --swap "$SWAP_MB"
      --rootfs "$STORAGE:$DISK_GB" --net0 "$NET0" --unprivileged "$UNPRIVILEGED"
      --features "$FEATURES" --onboot 1 --ostype debian --timezone host --tags blavingad
      --description "BLAVINGAD — autonomous Slack agent (systemd: blavingad.service)")
[ -n "$DNS" ] && ARGS+=(--nameserver "$DNS")
[ -n "$SSH_KEY" ] && ARGS+=(--ssh-public-keys "$SSH_KEY")
[ -n "$PASSWORD" ] && ARGS+=(--password "$PASSWORD")

say "Creating container $CTID ($HOSTNAME_CT, $CORES cores, ${MEMORY_MB} MB, ${DISK_GB} GB on $STORAGE)"
pct create "$CTID" "$TEMPLATE_STORAGE:vztmpl/$TEMPLATE" "${ARGS[@]}"
pct start "$CTID"

say "Waiting for the network"
for _ in $(seq 60); do
  pct exec "$CTID" -- getent hosts deb.debian.org >/dev/null 2>&1 && break
  sleep 2
done
pct exec "$CTID" -- getent hosts deb.debian.org >/dev/null 2>&1 || die "no network/DNS in the container (check BRIDGE/IP/GW/DNS)"
CT_IP=$(pct exec "$CTID" -- sh -c "hostname -I | awk '{print \$1}'")

# ---------- source, config, data ----------
if [ -z "$REPO" ]; then
  say "Packing the source from $SRC_DIR"
  tar -C "$SRC_DIR" --exclude=./.env --exclude='./.env.*' --exclude=./data --exclude=./workspace \
      --exclude=./bin --exclude=./blavingad --exclude='*.db' --exclude='*.db-*' -czf "$TMP/src.tgz" .
  pct push "$CTID" "$TMP/src.tgz" /root/blavingad-src.tgz
fi

if [ -n "$ENV_FILE" ]; then
  tr -d '\r' < "$ENV_FILE" > "$TMP/blavingad.env"
  grep -q '\$\$' "$TMP/blavingad.env" &&
    warn "the env file contains \"\$\$\" (docker compose escaping): systemd reads values literally, write a single \$"
elif [ -n "$REPO_ROOT" ] && [ -f "$REPO_ROOT/.env.example" ]; then
  cp "$REPO_ROOT/.env.example" "$TMP/blavingad.env"
else
  : > "$TMP/blavingad.env"
fi
# Addresses that only made sense on the old host point to the container now.
LOCAL_RE='localhost|127\.0\.0\.1|example\.com'
current() { grep -E "^$1=" "$TMP/blavingad.env" | tail -1 | cut -d= -f2- || true; }
{
  echo
  echo "# ---- added by create-lxc.sh ($(date -Iseconds)) ----"
  if [[ -z "$(current PUBLIC_URL)" || "$(current PUBLIC_URL)" =~ $LOCAL_RE ]]; then
    echo "PUBLIC_URL=http://$CT_IP:8080"
  fi
  if [[ -z "$(current DEPLOY_HOST)" || "$(current DEPLOY_HOST)" =~ $LOCAL_RE ]]; then
    echo "DEPLOY_HOST=$CT_IP"
  fi
  [ "$DOCKER" = 1 ] || echo "DOCKER_DEPLOY=false"
} >> "$TMP/blavingad.env"
pct push "$CTID" "$TMP/blavingad.env" /root/blavingad.env --perms 600
if [ "$TUNNEL" = 1 ] && [ -z "$CF_TUNNEL_TOKEN" ]; then
  CF_TUNNEL_TOKEN=$(current CLOUDFLARE_TUNNEL_TOKEN | sed -e 's/^"//' -e 's/"$//')
  case "$CF_TUNNEL_TOKEN" in vault://*) CF_TUNNEL_TOKEN="" ;; esac   # resolved by the agent, not here
fi

if [ -n "$DATA_TGZ" ]; then
  say "Pushing the data to migrate ($DATA_TGZ)"
  pct push "$CTID" "$DATA_TGZ" /root/blavingad-data.tgz --perms 600
fi

cat > "$TMP/provision.env" <<EOF
DOCKER='$DOCKER'
REPO='$REPO'
BRANCH='$BRANCH'
GIT_TOKEN='$GIT_TOKEN'
TUNNEL='$TUNNEL'
TUNNEL_TOKEN='$CF_TUNNEL_TOKEN'
EOF
pct push "$CTID" "$TMP/provision.env" /root/provision.env --perms 600

# ---------- provisioning (runs inside the container) ----------
cat > "$TMP/provision.sh" <<'PROVISION'
#!/usr/bin/env bash
set -euo pipefail
. /root/provision.env
rm -f /root/provision.env
# pct exec passes a minimal PATH and the host session's locale: set both.
export PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export LANG=C.UTF-8 LC_ALL=C.UTF-8
export DEBIAN_FRONTEND=noninteractive
say() { printf '\033[1;36m  ->\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m  xx\033[0m %s\n' "$*" >&2; exit 1; }
ARCH=$(dpkg --print-architecture)
. /etc/os-release

say "System packages"
apt-get update -q
apt-get install -y -q --no-install-recommends \
  ca-certificates curl git gnupg jq ripgrep sqlite3 unzip xz-utils zip openssh-client \
  python3 python3-pip python3-venv build-essential procps less nano tzdata locales
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg -o /etc/apt/keyrings/githubcli.gpg
echo "deb [arch=$ARCH signed-by=/etc/apt/keyrings/githubcli.gpg] https://cli.github.com/packages stable main" \
  > /etc/apt/sources.list.d/github-cli.list
PKGS=(gh)
if [ "$DOCKER" = 1 ]; then
  curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
  echo "deb [arch=$ARCH signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian $VERSION_CODENAME stable" \
    > /etc/apt/sources.list.d/docker.list
  PKGS+=(docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin)
fi
if [ "$TUNNEL" = 1 ]; then
  curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg -o /usr/share/keyrings/cloudflare-main.gpg
  echo "deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main" \
    > /etc/apt/sources.list.d/cloudflared.list
  PKGS+=(cloudflared)
fi
apt-get update -q
apt-get install -y -q --no-install-recommends "${PKGS[@]}"
[ "$DOCKER" = 1 ] && systemctl enable --now docker
if [ "$TUNNEL" = 1 ] && [ -n "$TUNNEL_TOKEN" ]; then
  say "Cloudflare Tunnel connector"
  cloudflared service install "$TUNNEL_TOKEN" >/dev/null 2>&1 || echo "  ⚠️ cloudflared service install failed: run it again from the container"
fi
TUNNEL_TOKEN=""

say "Go toolchain"
GOV=$(curl -fsSL 'https://go.dev/VERSION?m=text' | head -1)
rm -rf /usr/local/go
curl -fsSL "https://go.dev/dl/${GOV}.linux-${ARCH}.tar.gz" | tar -xz -C /usr/local
ln -sf /usr/local/go/bin/go /usr/local/bin/go
ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt

say "Node.js 22, wrangler, Bitwarden CLI, bun"
NARCH=$(echo "$ARCH" | sed 's/amd64/x64/')
NFILE=$(curl -fsSL https://nodejs.org/dist/latest-v22.x/SHASUMS256.txt | awk "/linux-${NARCH}.tar.xz/ {print \$2}")
curl -fsSL "https://nodejs.org/dist/latest-v22.x/${NFILE}" | tar -xJ -C /usr/local --strip-components=1
npm install -g -q wrangler @bitwarden/cli
npm cache clean --force >/dev/null 2>&1 || true
curl -fsSL https://bun.sh/install | BUN_INSTALL=/usr/local bash >/dev/null
hash -r
for tool in go node npm wrangler bw bun git gh; do
  command -v "$tool" >/dev/null || die "$tool is missing after the install"
done
say "go $(go env GOVERSION) · node $(node --version) · bw $(bw --version)"

say "Source code"
SEED=/opt/blavingad/src
rm -rf "$SEED" && mkdir -p "$SEED"
if [ -f /root/blavingad-src.tgz ]; then
  tar -xzf /root/blavingad-src.tgz -C "$SEED" && rm -f /root/blavingad-src.tgz
else
  URL=$REPO
  [ -n "$GIT_TOKEN" ] && URL=$(echo "$REPO" | sed "s#^https://#https://x-access-token:${GIT_TOKEN}@#")
  git clone -q --branch "$BRANCH" "$URL" "$SEED"
  git -C "$SEED" remote set-url origin "$REPO"
fi
git config --global safe.directory '*'

say "Building BLAVINGAD"
cd "$SEED"
VERSION="lxc-$(git rev-parse --short HEAD 2>/dev/null || date +%Y%m%d)"
CGO_ENABLED=0 GOFLAGS=-buildvcs=false go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
  -o /usr/local/bin/blavingad ./cmd/blavingad
/usr/local/bin/blavingad version

say "Data, config and service"
mkdir -p /data /workspace /etc/blavingad
install -m 600 /root/blavingad.env /etc/blavingad/blavingad.env && rm -f /root/blavingad.env
if [ -f /root/blavingad-data.tgz ]; then
  tar -xzf /root/blavingad-data.tgz -C /data && rm -f /root/blavingad-data.tgz
  rm -rf /data/src /data/src-* /data/source.path /data/bin   # rebuilt from this install
fi

cat > /etc/systemd/system/blavingad.service <<'UNIT'
[Unit]
Description=BLAVINGAD — autonomous Slack agent
Wants=network-online.target
After=network-online.target docker.service

[Service]
Type=simple
User=root
WorkingDirectory=/data
Environment=HOME=/root
Environment=DATA_DIR=/data
Environment=WORKSPACE_DIR=/workspace
Environment=SEED_DIR=/opt/blavingad/src
Environment=WEB_ADDR=:8080
Environment=GOFLAGS=-buildvcs=false
Environment=PIP_BREAK_SYSTEM_PACKAGES=1
Environment=PATH=/usr/local/go/bin:/root/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
# Overrides the defaults above. Values are read literally (no $ expansion).
EnvironmentFile=-/etc/blavingad/blavingad.env
ExecStart=/usr/local/bin/blavingad supervise
Restart=always
RestartSec=5
KillMode=mixed
TimeoutStopSec=60
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT

# Admin helper: same environment as the service (systemd parses the file).
cat > /usr/local/sbin/blavingadctl <<'CTL'
#!/bin/sh
case "$1" in
  update) shift
    exec systemd-run --quiet --pipe --wait --collect -p WorkingDirectory=/data \
      -p EnvironmentFile=/etc/blavingad/blavingad.env -E HOME=/root -E DATA_DIR=/data \
      -E WORKSPACE_DIR=/workspace -E GOFLAGS=-buildvcs=false \
      -E PATH=/usr/local/go/bin:/root/go/bin:/usr/local/bin:/usr/bin:/bin \
      /usr/local/bin/blavingad update "$@" ;;
  logs) shift; exec journalctl -u blavingad -f "$@" ;;
  status) exec systemctl status blavingad ;;
  restart) exec systemctl restart blavingad ;;
  env) "${EDITOR:-nano}" /etc/blavingad/blavingad.env && systemctl restart blavingad ;;
  *) echo "usage: blavingadctl update [--pull] [--force] | logs | status | restart | env"; exit 2 ;;
esac
CTL
chmod 755 /usr/local/sbin/blavingadctl

systemctl daemon-reload
systemctl enable --now blavingad
PROVISION
pct push "$CTID" "$TMP/provision.sh" /root/provision.sh --perms 700

say "Provisioning (packages, Go, Node, build…) — a few minutes"
pct exec "$CTID" -- bash /root/provision.sh
pct exec "$CTID" -- rm -f /root/provision.sh

say "Waiting for the web UI"
OK=0
for _ in $(seq 45); do
  if pct exec "$CTID" -- curl -fsS -m 3 http://127.0.0.1:8080/api/health >/dev/null 2>&1; then OK=1; break; fi
  sleep 2
done
[ "$OK" = 1 ] || warn "the web UI does not answer yet: pct exec $CTID -- journalctl -u blavingad -n 80"

if [ "$TUNNEL" != 1 ]; then TUNNEL_LINE="disabled (TUNNEL=0)"
elif [ -n "$CF_TUNNEL_TOKEN" ]; then TUNNEL_LINE="cloudflared running: the agent publishes apps with tunnel_route_add"
else TUNNEL_LINE="cloudflared installed: ask the agent \"set up the Cloudflare tunnel\""
fi
CF_TUNNEL_TOKEN=""

cat <<EOF

✅ BLAVINGAD is installed in container $CTID ($HOSTNAME_CT)

  Web UI      http://$CT_IP:8080   (if PUBLIC_URL was not set, it was set to this address)
  Shell       pct enter $CTID
  Logs        pct exec $CTID -- blavingadctl logs
  Config      pct exec $CTID -- blavingadctl env        (edits /etc/blavingad/blavingad.env, restarts)
  Update      pct exec $CTID -- blavingadctl update --pull
  Data        /data (DB, memory, source, self-built binaries) · /workspace (projects)
  Tunnel      $TUNNEL_LINE

The agent runs as root in the container. Shell commands still go through
approval: set the shell policy to Auto in Tools if you want it unattended.
EOF
[ "$IP" = dhcp ] && echo "⚠️  The IP comes from DHCP: reserve it (or set IP=… GW=…) so PUBLIC_URL stays valid."
exit 0
