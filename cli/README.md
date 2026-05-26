# LaziRouter - FREE AI Router & Token Saver

**Never stop coding. Save 20-40% tokens with RTK + auto-fallback to FREE & cheap AI models.**

**Connect All AI Code Tools (Claude Code, Cursor, Antigravity, Copilot, Codex, Gemini, OpenCode, Cline, OpenClaw...) to 40+ AI Providers & 100+ Models.**

[![npm](https://img.shields.io/npm/v/lazirouter.svg)](https://www.npmjs.com/package/lazirouter)
[![Downloads](https://img.shields.io/npm/dm/lazirouter.svg)](https://www.npmjs.com/package/lazirouter)
[![Docker Pulls](https://img.shields.io/docker/pulls/decolua/lazirouter.svg?logo=docker&label=Docker%20pulls)](https://hub.docker.com/r/decolua/lazirouter)
[![GHCR](https://img.shields.io/badge/GHCR-decolua%2Flazirouter-blue?logo=github)](https://github.com/decolua/lazirouter/pkgs/container/lazirouter)
[![License](https://img.shields.io/npm/l/lazirouter.svg)](https://github.com/decolua/lazirouter/blob/main/LICENSE)

<a href="https://trendshift.io/repositories/22628" target="_blank"><img src="https://trendshift.io/api/badge/repositories/22628" alt="decolua%2Flazirouter | Trendshift" style="width: 250px; height: 55px;" width="250" height="55"/></a>

[🌐 Website](https://lazirouter.com) • [📖 Full Docs](https://github.com/decolua/lazirouter)

---

## 🤔 Why LaziRouter?

**Stop wasting money, tokens and hitting limits:**

- ❌ Subscription quota expires unused every month
- ❌ Rate limits stop you mid-coding
- ❌ Tool outputs (git diff, grep, ls...) burn tokens fast
- ❌ Expensive APIs ($20-50/month per provider)

**LaziRouter solves this:**

- ✅ **RTK Token Saver** - Auto-compress tool_result, save 20-40% tokens
- ✅ **Maximize subscriptions** - Track quota, use every bit before reset
- ✅ **Auto fallback** - Subscription → Cheap → Free, zero downtime
- ✅ **Multi-account** - Round-robin between accounts per provider
- ✅ **Universal** - Works with any OpenAI/Claude-compatible CLI

---

## ⚡ Quick Start

**Option 1 — npm (recommended for desktop):**

```bash
npm install -g lazirouter
lazirouter

# Or run directly with npx
npx lazirouter
```

**Option 2 — Docker (server/VPS):**

```bash
docker run -d --name lazirouter -p 20128:20128 \
  -v "$HOME/.lazirouter:/app/data" -e DATA_DIR=/app/data \
  decolua/lazirouter:latest
```

Published images: [Docker Hub](https://hub.docker.com/r/decolua/lazirouter) • [GHCR](https://github.com/decolua/lazirouter/pkgs/container/lazirouter) (multi-platform amd64/arm64).

🎉 Dashboard opens at `http://localhost:20128`

**2. Connect a FREE provider (no signup needed):**

Dashboard → Providers → Connect **Kiro AI** (free Claude unlimited) or **OpenCode Free** (no auth) → Done!

**3. Use in your CLI tool:**

```
Claude Code/Codex/OpenClaw/Cursor/Cline Settings:
  Endpoint: http://localhost:20128/v1
  API Key:  [copy from dashboard]
  Model:    kr/claude-sonnet-4.5
```

That's it! Start coding with FREE AI models.

---

## 🚀 CLI Options

```bash
lazirouter                    # Start with default settings
lazirouter --port 8080        # Custom port
lazirouter --no-browser       # Don't open browser
lazirouter --skip-update      # Skip auto-update check
lazirouter --help             # Show all options
```

**Dashboard**: `http://localhost:20128/dashboard`

---

## 🛠️ Supported CLI Tools

Claude-Code • OpenClaw • Codex • OpenCode • Cursor • Antigravity • Cline • Continue • Droid • Roo • Copilot • Kilo Code • Gemini CLI • Qwen Code • iFlow • Crush • Crusher • Aider

Any tool supporting OpenAI/Claude-compatible API works.

---

## 💾 Data Location

- **macOS/Linux**: `~/.lazirouter/db/data.sqlite`
- **Windows**: `%APPDATA%/lazirouter/db/data.sqlite`
- **Docker**: `/app/data/db/data.sqlite` (mount `$HOME/.lazirouter` to persist)

---

## 📚 Documentation

Full docs, advanced setup, video tutorials & development guide:

- **GitHub**: https://github.com/decolua/lazirouter
- **Full README**: https://github.com/decolua/lazirouter/blob/main/app/README.md
- **Website**: https://lazirouter.com

---

## 🙏 Acknowledgments

- **[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)** - Original Go implementation

## 📄 License

MIT License - see [LICENSE](LICENSE) for details.
