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
| **Media Processing** | `media.go`, `media_downloader.go`, `media_logger.go` | 25-transformation FFmpeg interactive picker (`/media transform`), stream & clip ingestion via yt-dlp (`/media download`), format transcoding, lossless optimization, and LUFS normalization. |
| **Fleet & System Ops** | `docker_manager.go`, `fleet_manager.go`, `sysinfo.go` | Ephemeral Docker container fleet manager (`/docker`), Git fleet health and dirty tree monitor (`/fleet`), and NVIDIA GPU / thermal telemetry (`/sysinfo`). |
| **AI & Multi-Provider** | `providers/`, `venice/`, `a2e/`, `openrouter.go` | Venice AI suite (`/venice music` Minimax v26, chat, image, transcription), OpenRouter streaming LLM gateway, and A2E audio integration. |
| **Reminders & Workers** | `reminders.go`, `service.go`, `health_checker.go` | Cancellable background workers; recurring reminders with duration intervals (`30m`, `1h`, `1d`, `1w`) and timezone support; external URL health probing. |
| **Audit & Moderation** | `automod.go`, `message_logger.go`, `message_log.go`, `data_pruner.go`, `log_analytics.go` | Substring AutoMod with event logging to `Data/AutoModEvents/`; local JSONL message archiving; automated rolling retention, gzip archiving to `Data/Archives/`, size-based media pruning (`/prunelogs`), and DuckDB SQL analytics (`/message-log query`). |

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
- [x] **Phase 3 Completed:**
  - Unified subpackage configuration resolution across `a2e/`, `providers/`, `venice/`, and `modules/` via `SUNFIRE_CONFIG_FILE`.
  - Built automated rolling retention, size-based media pruning, and gzip archiving daemon (`data_pruner.go`, `/prunelogs`).
  - Added native pure-Go PDF resume extraction (`github.com/dslipak/pdf`) to `/career analyze` and `/career match` with support for uploaded resume attachments.
- [x] **Host Tooling & Fleet Integration Completed:**
  - 🎮 Integrated live NVIDIA GPU model, VRAM budget, temperature (°C), and core compute load telemetry into `/sysinfo` (`sysinfo.go`, `sysinfo_unix.go`, `sysinfo_windows.go`, `sysinfo_test.go`).
  - 🐳 Built ephemeral Docker Fleet & Container Manager (`/docker status`, `restart`, `start`, `stop`, `logs`) (`docker_manager.go`, `docker_manager_test.go`).
  - 🌐 Built Git Fleet Health & Multi-Repo Monitor (`/fleet status`, `repo`) scanning all 14 repositories with GPG verification indicators (`fleet_manager.go`, `fleet_manager_test.go`).
  - 🎬 Built Stream & Clip Ingestion via yt-dlp (`/media download`) with FFmpeg compression under 25 MiB for Discord attachments (`media_downloader.go`, `media_downloader_test.go`).
  - 🦆 Built High-Speed Message Log Analytics via DuckDB (`/message-log query`) with box-table formatting, 5 analytical presets, and read-only custom SQL validation (`log_analytics.go`, `log_analytics_test.go`).
  - Recompiled binary, restarted `sunfire.service`, and verified all daemons and handlers healthy.
- [x] **Setup Workflow Idempotency & NIST NVD Feed Channel:**
  - Expanded `/setup` workflow to include the `#nist` channel (National Vulnerability Database: https://nvd.nist.gov/) under the read-only `Feeds & Media` category.
  - Implemented channel auditing (`AuditSetupWorkflow`) so running `/setup` again inspects the server, verifies existing channels, and automatically creates any missing channels or categories without duplicating existing ones.
  - Added `NistEnabled`, `NistChannelID`, and `NistRoleID` to `GuildSettings` (`guild_config.go`) and forward-compatible `NistAPIKey` and `NistPollIntervalMs` to `Config` (`config.go`).
  - Updated `/notifiers status` embed and description to include NIST NVD configuration state.
  - Added automated unit test suite in `setup_test.go` verifying catalog definitions, full-missing scenario, partial/NIST detection, and JSON configuration serialization.
  - Recompiled binary and restarted systemd user service (`sunfire.service`).
- [x] **Career Status Export Option (`/career status export`):**
  - Added `export` boolean option to `/career status` slash-command spec.
  - Built pure-Go executive Markdown report generator (`ExportCareerStatusMarkdown`) detailing KPI summaries, urgent follow-up queue, full application directory, checklist items, and certification roadmap.
  - Built pure-Go vector PDF generator (`ExportCareerStatusPDF`) with navy branding, KPI metric cards, zebra-striped application table, colored status badges, follow-up alerts, and multi-page pagination.
  - Attached both `.md` and `.pdf` files to ephemeral Discord response for instant user download.
  - Added comprehensive test suites in `career_export_test.go` and verified with 100% test pass.
- [x] Completely updated core repository documentation: [README.md](README.md), [AGENTS.md](AGENTS.md), [HANDOFF.md](HANDOFF.md), and [ROADMAP.md](ROADMAP.md).

---

## In Progress & Immediate Next Steps

1. **Operational Maintenance & Reliability Monitoring:**
   - Supervise continuous bot uptime and background worker health under `systemd --user` (`sunfire.service`).
   - Monitor automated daily rolling log pruning and gzip archiving (`startPeriodicDataPruner`).
   - Track hourly subscription renewal DM alerts and periodic media deduplication passes.

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
