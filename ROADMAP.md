# Roadmap: Sunfire

## Goal & Architecture Principles

- **Primary Objective:** Serve as the definitive, robust Discord operations hub, personal productivity companion, and AI gateway for the dedicated Sunfire guild (`1557930395868209162`).
- **Key Principles & Constraints:**
  - **Deterministic Lifecycle:** Strictly linear startup, explicit worker lifecycle management with cancellable contexts, and bounded shutdown drain timeouts.
  - **Reliable Background Execution:** At-least-once delivery for notification pollers; state persistence before or immediately after Discord acceptance; resilient recovery from external service outages.
  - **Modular AI & Media Architecture:** Pluggable AI backends (Venice, OpenRouter, A2E) and local FFmpeg processing without hardcoding single provider dependencies.
  - **Data Privacy & Security:** Sensitive credentials isolated in `.private/config.json`; message logs and personal data stored strictly in local `Data/`; mandatory cryptographic GPG commit signing (`daniel@danielkill.online`).

---

## Phase 0 — Design, Gateway Bootstrap & Command Dispatch (COMPLETED)

- [x] Establish Go architecture with `discordgo` targeting Discord Gateway v10.
- [x] Build linear process bootstrap in `main.go` with signal trapping (`SIGINT`, `SIGTERM`).
- [x] Implement cancellable worker lifecycle manager (`service.go`) with drain timeouts.
- [x] Build slash command spec registry and guild-scoped command registration (`commands.go`).
- [x] Implement fine-grained permission model (`command_access.go`) distinguishing public, owner-only, and grantable restricted commands.

**Exit Criteria Met:** Stable Discord Gateway connection with clean worker lifecycle management and secure command dispatch.

---

## Phase 1 — Moderation, Media Processing & Notifier Daemons (COMPLETED)

- [x] Implement AutoMod engine (`automod.go`) with banned phrase detection and event logging to `Data/AutoModEvents/`.
- [x] Build local message and media logging subsystem with optional PostgreSQL archive querying (`/message-log stats`).
- [x] Implement interactive FFmpeg media transformation picker (`media.go`) supporting 25 image, audio, and video transformations.
- [x] Build periodic background notifier daemons:
  - YouTube upload notifications.
  - Kick livestream notifications (with offline edit state tracking).
  - Spotify release notifications.
  - Movie release alerts and game freebies.
  - Hermes Agent tips and birthday announcements.
  - OpenRouter new model release announcements.
- [x] Implement external URL health checking daemon (`health_checker.go`).

**Exit Criteria Met:** Core server moderation, rich media processing, and automated community notifier daemons fully operational.

---

## Phase 2 — AI Multi-Provider Integration & Productivity Engines (COMPLETED)

- [x] Integrate Venice AI suite (`venice/`):
  - `/venice music` (Minimax v26 music synthesis with cost quoting and audio cleanup).
  - Text chat, image generation, and audio transcription.
- [x] Integrate OpenRouter streaming LLM gateway (`openrouter.go`).
- [x] Integrate A2E audio generation and task polling (`a2e/`).
- [x] Build recurring reminder engine (`reminders.go`) with duration intervals (`30m`, `1h`, `1d`, `1w`) and timezone support.
- [x] Build Sunfire Career Copilot (`career.go`):
  - Job application tracking pipeline (`/career track`, `/career list`).
  - AI resume fit analysis (`/career analyze`).
  - Automated cover letter generator (`/career cover-letter`).
  - Certification roadmap tracking (`/career certs`).
  - Action item checklist and follow-up alerts (`/career checklist`, `/career followups`).
- [x] Build Subscriptions & Recurring Bills tracking suite (`subscriptions.go`):
  - Financial burn rate dashboard (`/subscriptions stats`).
  - Hourly scanner with 7-day expiration DM alerts.
  - Interactive Discord action buttons (`[✅ Mark as Renewed]`, `[🛑 Cancelled]`).
- [x] Deploy and verify production systemd user service (`~/.config/systemd/user/sunfire.service`).
- [x] Completely overhaul repository documentation:
  - [README.md](README.md) — Comprehensive system architecture, capabilities, and setup guides.
  - [AGENTS.md](AGENTS.md) — Mandatory GPG signing (`daniel@danielkill.online`), non-elevation, and Go standards.
  - [HANDOFF.md](HANDOFF.md) — Active operational status, configurations, and immediate next steps.
  - [ROADMAP.md](ROADMAP.md) — Multi-phase milestones and architecture roadmap.

**Exit Criteria Met:** Full productivity suites (Career Copilot, Subscriptions) and AI integrations active and running under systemd with complete documentation sync.

---

## Phase 3 — Configuration Unification & Log Rotation (PLANNED)

- [ ] **Unify Subpackage Configuration Lookups:**
  - Update `a2e/`, `providers/`, and `venice/` loaders to consistently resolve configuration from `SUNFIRE_CONFIG_FILE` rather than hardcoding relative `.private/config.json` paths.
- [ ] **Automated Data Pruning & Archiving Daemon:**
  - Implement rolling retention or size-based archiving for message and media logs under `Data/` to prevent unbounded disk growth.
- [ ] **Career Copilot PDF Resume Parsing:**
  - Add native PDF resume text extraction to `/career analyze` for automated skill and experience comparison against job postings.

**Exit Criteria:** Single unified configuration path across all subpackages; automated data pruning active; PDF resume ingestion operational.

---

## Phase 4 — Web Dashboard & Fleet Telemetry (PLANNED - Future)

- [ ] **Sunfire Management Web Dashboard:**
  - Build an optional local web UI displaying subscription burn rates, upcoming renewals, job application Kanban boards, and server health.
- [ ] **Outbound Webhook Notifications:**
  - Add webhook dispatchers to alert external monitoring systems or phone notifications upon critical bot events or server outages.
