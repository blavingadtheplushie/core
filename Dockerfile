# syntax=docker/dockerfile:1.7
ARG GO_VERSION=1.26

# ---------- build ----------
FROM golang:${GO_VERSION}-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/blavingad ./cmd/blavingad

# ---------- runtime ----------
# The runtime keeps the Go toolchain on purpose: BLAVINGAD rebuilds itself.
FROM golang:${GO_VERSION}-trixie

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git gnupg jq ripgrep sqlite3 tini unzip xz-utils zip \
      openssh-client python3 python3-pip python3-venv build-essential \
 && install -m 0755 -d /etc/apt/keyrings \
 && curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian $(. /etc/os-release && echo $VERSION_CODENAME) stable" > /etc/apt/sources.list.d/docker.list \
 && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg -o /etc/apt/keyrings/githubcli.gpg \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list \
 && apt-get update && apt-get install -y --no-install-recommends docker-ce-cli docker-buildx-plugin gh \
 && rm -rf /var/lib/apt/lists/*

# Node.js (latest 22 LTS) + wrangler, the Bitwarden CLI (Vaultwarden), and bun.
RUN ARCH=$(dpkg --print-architecture | sed 's/amd64/x64/') \
 && FILE=$(curl -fsSL https://nodejs.org/dist/latest-v22.x/SHASUMS256.txt | awk "/linux-${ARCH}.tar.xz/ {print \$2}") \
 && curl -fsSL "https://nodejs.org/dist/latest-v22.x/${FILE}" | tar -xJ -C /usr/local --strip-components=1 \
 && npm install -g wrangler @bitwarden/cli && npm cache clean --force \
 && curl -fsSL https://bun.sh/install | BUN_INSTALL=/usr/local bash \
 && node --version && wrangler --version && bw --version && bun --version

COPY --from=build /out/blavingad /usr/local/bin/blavingad
# Seed of the agent's own source tree (copied to /data/src on first boot).
COPY . /opt/blavingad/src
# Warm the module and build caches so its first self-build is fast.
RUN cd /opt/blavingad/src && go mod download && CGO_ENABLED=0 go build -o /dev/null ./cmd/blavingad

ENV DATA_DIR=/data \
    WORKSPACE_DIR=/workspace \
    SEED_DIR=/opt/blavingad/src \
    WEB_ADDR=:8080 \
    GOFLAGS=-buildvcs=false \
    PIP_BREAK_SYSTEM_PACKAGES=1
VOLUME ["/data", "/workspace"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=40s CMD curl -fsS http://127.0.0.1:8080/api/health || exit 1

ENTRYPOINT ["tini", "--", "blavingad"]
CMD ["supervise"]
