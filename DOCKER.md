# Docker

Run LaziRouter in a container. Published image: [`decolua/lazirouter`](https://hub.docker.com/r/decolua/lazirouter) — multi-platform `linux/amd64` + `linux/arm64`.

---

# 👤 For Users

## Quick start

```bash
docker run -d \
  -p 20128:20128 \
  -v "$HOME/.lazirouter:/app/data" \
  -e DATA_DIR=/app/data \
  --name lazirouter \
  decolua/lazirouter:latest
```

App listens on port `20128`. Open: http://localhost:20128

## Manage container

```bash
docker logs -f lazirouter        # view logs
docker stop lazirouter           # stop
docker start lazirouter          # start again
docker rm -f lazirouter          # remove
```

## Data persistence

```bash
-v "$HOME/.lazirouter:/app/data" \
-e DATA_DIR=/app/data
```

Without `DATA_DIR`, the app falls back to `~/.lazirouter/` (macOS/Linux) or `%APPDATA%\lazirouter\` (Windows). In the container, `DATA_DIR=/app/data` makes the bind mount work.

Data layout under `$DATA_DIR/`:

```text
$DATA_DIR/
├── db/
│   ├── data.sqlite       # main SQLite database
│   └── backups/          # auto backups
└── ...                   # certs, logs, runtime configs
```

Host path: `$HOME/.lazirouter/db/data.sqlite`
Container path: `/app/data/db/data.sqlite`

## Optional env vars

```bash
docker run -d \
  -p 20128:20128 \
  -v "$HOME/.lazirouter:/app/data" \
  -e DATA_DIR=/app/data \
  -e PORT=20128 \
  -e HOSTNAME=0.0.0.0 \
  -e DEBUG=true \
  --name lazirouter \
  decolua/lazirouter:latest
```

## Update to latest

```bash
docker pull decolua/lazirouter:latest
docker rm -f lazirouter
# re-run the quick start command
```

---

# 🛠 For Developers

## Build image locally (test)

```bash
cd app && docker build -t lazirouter .

docker run --rm -p 20128:20128 \
  -v "$HOME/.lazirouter:/app/data" \
  -e DATA_DIR=/app/data \
  lazirouter
```

## Publish (automatic via CI)

Push a git tag `v*` → GitHub Actions builds multi-platform (amd64+arm64) and pushes to:
- `ghcr.io/decolua/lazirouter:v{version}` + `:latest`
- `decolua/lazirouter:v{version}` + `:latest`

```bash
# Use scripts/release.js (recommended)
node scripts/release.js "Release title" "Notes"

# Or manually
git tag v0.4.x && git push origin v0.4.x
```

Workflow: `app/.github/workflows/docker-publish.yml`
