# Active Task Handoff: Sunfire

## Current Operational State

Sunfire is deployed and actively running as a **systemd user service** (`sunfire.service`) on host `danielpredpc` (WSL2 Linux environment) under user `daniel`.

- **Primary Guild:** `1557930395868209162`
- **Bot Identity:** `Sunfire` (App ID: `1557934676457558086`)
- **Systemd Unit:** `/home/daniel/.config/systemd/user/sunfire.service` (Active, enabled, supervised with journald)
- **Active Workers:** Reminders, Subscriptions, Career Copilot, Health Checker, Media Deduplication, and RSS Notifiers (regularly dispatching live OpenRouter AI model updates).
- **Test Suite Status:** 100% passing across all packages (`sunfire`, `sunfire/a2e`, `sunfire/providers`, `sunfire/venice`).
- **Documentation State:** Comprehensive documentation suite aligned with live service architecture ([README.md](README.md), [AGENTS.md](AGENTS.md), [ROADMAP.md](ROADMAP.md)).

---

## Subsystem & Feature Matrix

| Subsystem | Source Files | Capabilities & Mechanics |
|---|---|---|
| **Gateway & Dispatch** | `main.go`, `bot.go`, `commands.go` | DiscordGo session lifecycle; guild-scoped slash command registration; public vs restricted access control (`command_access.go`). |
| **Career Copilot** | `career.go`, `career_test.go` | Job application pipeline (`/career track`, `/career list`), AI resume fit analysis (`/career analyze`), cover letter generator (`/career cover-letter`), certification roadmap (`/career certs`), and daily follow-up alerts (`/career followups`). |
| **Subscriptions & Bills** | `subscriptions.go` | Recurring bill tracking (`/subscriptions`), monthly burn rate dashboard, hourly scanner, 7-day expiration DM alerts with interactive action buttons (`[✅ Mark as Renewed]`, `[🛑 Cancelled]`). |
| **Media Processing** | `media.go`, `media_logger.go` | 25-transformation FFmpeg interactive picker (`/media`), format transcoding (MP4, WebM, MP3, Opus), lossless optimization, GIF creation, and LUFS normalization. |
| **AI & Multi-Provider** | `providers/`, `venice/`, `a2e/`, `openrouter.go` | Venice AI suite (`/venice music` Minimax v26, chat, image, transcription), OpenRouter streaming LLM gateway, and A2E audio integration. |
| **Reminders & Workers** | `reminders.go`, `service.go`, `health_checker.go` | Cancellable background workers; recurring reminders with duration intervals (`30m`, `1h`, `1d`, `1w`) and timezone support; external URL health probing. |
| **Audit & Moderation** | `automod.go`, `message_logger.go`, `message_log.go` | Substring AutoMod with event logging to `Data/AutoModEvents/`; local JSONL message archiving; optional PostgreSQL message archive querying. |

---

## Completed Milestones

- [x] Initialized repository, Git configuration, and `.private/` secret protection.
- [x] Implemented core DiscordGo session management with graceful teardown and worker drain timeouts.
- [x] Built central slash-command composition and access grant system (`Data/commandAccess.json`).
- [x] Implemented interactive FFmpeg media transformation picker (`/media`) with 25 operations.
- [x] Built Career Copilot suite with application tracking, AI fit analysis, and certification roadmap.
- [x] Built Subscriptions tracking system with 7-day DM alerts and interactive renewal action buttons.
- [x] Integrated Venice AI suite (`/venice music`, image, chat, transcription) and OpenRouter streaming.
- [x] Implemented recurring reminder scheduler with timezone support.
- [x] Built AutoMod engine and dual local/PostgreSQL message archiving.
- [x] Deployed and verified production systemd user service (`~/.config/systemd/user/sunfire.service`).
- [x] Completely updated core repository documentation: [README.md](README.md), [AGENTS.md](AGENTS.md), [HANDOFF.md](HANDOFF.md), and [ROADMAP.md](ROADMAP.md).

---

## In Progress & Immediate Next Steps

1. **Unify Subpackage Configuration Resolution:**
   - Update `a2e/`, `providers/`, and `venice/` loaders to consistently resolve configuration from `SUNFIRE_CONFIG_FILE` instead of relying on repo-relative `.private/config.json` paths.
2. **Automated Log Rotation & Pruning Policy:**
   - Implement an automated rolling retention daemon for message and media logs in `Data/` to prevent unbounded disk growth.
3. **Career Copilot PDF Resume Parsing:**
   - Add direct PDF resume ingestion to `/career analyze` for automated skill extraction and comparison against job descriptions.

---

## Blockers & Gotchas

- **Windows Plan9 / 9P File Locking:**
  - Running Go toolchain commands from Windows against WSL repository paths causes `RLock ...: Incorrect function`. Always run Go commands inside WSL2 using `/usr/local/go/bin/go`.
- **Discord Privileged Intents:**
  - The bot requires **Server Members Intent** and **Message Content Intent** enabled in the Discord Developer Portal under Bot settings.
- **Git Commit Signing:**
  - Execute `git commit -S` inside WSL2 (`wsl.exe -d daniel-kill -e bash -c ...`) to utilize the active GPG agent and avoid Windows GUI pinentry prompt timeouts.
- **Remote Push Policy:**
  - Never push commits to remote repositories without explicit user confirmation.
