# Sunfire

Sunfire is the personal Discord bot for the dedicated Sunfire server (`1557930395868209162`). It is implemented in Go using `discordgo` and combines moderation, slash-command automation, AI provider access, scheduled daemons, and operational telemetry.

This is the internal developer guide for building, testing, extending, and operating Sunfire.

## Runtime profile

The bot is not a generic public bot. It is tightly scoped to one Discord guild and a single operational environment.

- Primary server: `1557930395868209162`
- Bot owner: `Daniel Kill` (`daniel@danielkill.online`)
- Runtime model: long-lived Discord Gateway client with cancellable background workers
- Secrets: root config uses `.private/config.json` by default or `SUNFIRE_CONFIG_FILE`; integration packages have separate config-path behavior described below
- Logs: emitted to standard output/error (journald under systemd) and optionally to a Discord logging channel

## High-level architecture

Startup is intentionally linear and explicit:

1. `main.go` resolves the bot token
2. `a2e.Init()`, `venice.Init()`, and `providers.Init()` initialize subpackages
3. `buildCommandSpecs()` populates command metadata
4. `NewBot(token)` creates the `discordgo` session and binds handlers
5. `bot.Open()` opens the Gateway connection; `main.go` waits for SIGINT/SIGTERM and then closes the bot

The core bot object is defined in `bot.go`:

- `Bot{ Session, logger, registered, workers }`
- `NewBot()` wires all Discord event handlers
- `onReady()` registers commands, logs startup, and starts the worker lifecycle
- `onInteractionCreate()` dispatches slash commands and modal/component interactions

Command specs are assembled by `buildCommandSpecs()` before the session opens. Discord's Ready event performs guild-specific command registration and starts the worker set. A lifecycle guard prevents workers from being started a second time after a reconnect. Shutdown cancels workers, closes the session, and waits up to ten seconds for worker completion.

## Repository structure

| Path | Responsibility |
| --- | --- |
| `main.go`, `bot.go` | Process bootstrap, Discord session, event handlers, worker orchestration |
| `commands.go` | Slash-command definitions, composition, registration, and core dispatch |
| `config.go`, `paths.go` | Runtime config and configurable application/data paths |
| `service.go` | Cancellable periodic-work and worker lifecycle helpers |
| `providers/` | Provider clients and provider-backed Discord commands |
| `venice/`, `a2e/` | Venice and audio integrations, handlers, and command specs |
| `modules/` | Reusable Go modules and AI-related functionality |
| `notifier_*.go` | External polling integrations and their persisted delivery caches |
| `message_logger.go`, `message_log.go`, `media_logger.go` | Local message/media archives and optional PostgreSQL message archive |
| Other root Go files | Feature-specific commands and workflows (moderation, tickets, birthdays, games, and more) |
| `*_test.go` | Unit and integration-style tests; most tests use local fixtures or temporary directories |
| `deploy/sunfire.service` | Example hardened Linux systemd unit |
| `Data/`, `.private/` | Runtime state and secrets; local defaults, not source-controlled |

When navigating a feature, start at its command spec/handler, follow shared dispatch in `commands.go` or `bot.go`, then inspect the feature's persistence helpers and tests. For AI provider work, start with the provider-specific package and its command specs before changing root dispatch.

## Configuration and secret handling

The bot reads local configuration from `.private/config.json` using `config.go`.

The optional PostgreSQL message archive reads `POSTGRES_CONNECTION_STRING` from
that config file or the environment. A non-empty config value takes precedence;
otherwise the environment variable is used. For example, deployments can set
the variable in the service manager instead of storing a database URL in JSON.
Sunfire forces `sslmode=disable` for this connection because the configured
PostgreSQL server does not support TLS; use this only on a trusted private
network.
`/message-log stats` reports configuration, connection, or query errors when
the database cannot be reached.

### Precedence

For the Discord token, precedence is:

1. `.private/config.json` (`botToken`)
2. `BOT_TOKEN` environment variable
3. otherwise a startup failure is triggered

This behavior is intentionally explicit and is defined in `config.go`.

### Local config shape

`Config` in `config.go` is the source of truth for JSON field names. It contains bot identity/channel settings, provider and notifier credentials, the optional health-check configuration, scrape settings, and the primary guild's feature settings. Provider credentials are grouped by provider where the struct defines a `ProviderConfig`; notifier keys use their integration-specific JSON tags. Only populate credentials for integrations you intend to enable.

An absent config file is treated as an empty config, so `BOT_TOKEN` can be sufficient to start the process, but most commands and background integrations will remain unconfigured. Malformed JSON and file permission errors fail config loading.

Secrets are intentionally not committed to the repo. Local runs can use `.private/config.json`; packaged deployments should provision a protected config file and set `SUNFIRE_CONFIG_FILE`.

### Runtime paths

The application supports configurable locations for packaged deployments:

- `SUNFIRE_HOME`: application/assets root. Defaults to the process working directory.
- `SUNFIRE_DATA_DIR`: writable persistent state directory. Defaults to `$SUNFIRE_HOME/Data`.
- `SUNFIRE_CONFIG_FILE`: config JSON path. Defaults to `$SUNFIRE_HOME/.private/config.json` (with a source-tree fallback for local development).
- `SUNFIRE_HERMES_BIN`: optional absolute path to the Hermes executable. If unset, Sunfire searches `PATH` and then `~/.local/bin/hermes` for the service user.

Set these in the service manager to absolute paths. Persistent feature state, notifier caches, message/media logs, and backups use `SUNFIRE_DATA_DIR`; asset lookups use `SUNFIRE_HOME`. Keep the config file readable by the service account and writable only where the application is expected to persist config-backed settings.

### Local setup

Requirements: the Go version declared in `go.mod` (currently Go 1.26.0), a Discord application/bot token, and access to the target guild. Optional AI, notifier, and database features also require their corresponding credentials/services.

From the repository root:

```bash
go test ./...
go build -o Sunfire .
```

For a local run, either create `.private/config.json` from the fields in `config.go`, or provide the Discord token in the environment:

```bash
BOT_TOKEN=your-token go run .
```

Do not put real tokens in shell history, screenshots, issue reports, or committed config files. For production, provision config and persistent directories through the service manager; see [Linux systemd deployment](#linux-systemd-deployment).

## Core runtime features

### Command system

The command system is built around registered Discord slash commands. `buildCommandSpecs()` populates command definitions before bot creation and `registerCommands` registers them with Discord.

There are public commands and protected commands. `isPublicCommand()` classifies which slash commands are open to the wider server and which are restricted.

`/media file:<attachment>` downloads an image, audio, or video attachment and presents a private picker with up to 25 FFmpeg transformations for that media type. Images and videos include **Resize...** (enter a width and optional height; blank height preserves aspect ratio) and **Optimize (lossless)**. Optimization tries lossless PNG for still images and lossless H.264 with copied audio for video, returning the original for animated images or when the candidate is not smaller; lossless compression cannot guarantee a smaller result for every input. The transformed file replaces the picker response. Input and output files are limited to 25 MiB; unselected media is removed after 15 minutes. The bot host must have both `ffmpeg` and `ffprobe` installed.

`Important command-related files:

- `commands.go`
- `media.go`
- `command_access.go`
- `command_access_test.go`
- `guild_config.go`

`/emojigg search`, `/emojigg list`, `/emojigg random`, and
`/emojigg preview asset:<name-or-ID>` browse the local
`emojis/` asset directory under `SUNFIRE_HOME` (the application root).
Search and list show 15 results per page with Previous/Next buttons; an
optional `page` value is also available when running the command. Use
`/emojigg add asset:<name-or-ID>` to add one to the server; an optional `name`
sets its server emoji name. Adding requires the invoker to have Manage
Expressions or Administrator permission, and the bot must also be allowed to
manage server expressions. Discord's emoji upload limit applies, so the
encoded image must be smaller than 256 KiB and the server must have an
available emoji slot.

### Reminders

`/remind add` accepts relative times such as `10m`, `1h30m`, or `in 2h`, local date/time values such as `09/29/2026 at 14:30`, and ISO 8601 timestamps with an explicit offset. Local date/time values use `America/New_York` by default; set the optional `timezone` to an IANA zone (for example, `America/Los_Angeles` or `UTC`) to use another location. Relative times are elapsed durations, while exact local times are interpreted in the selected zone.

The optional `repeat` setting accepts a positive duration such as `30m`, `1h`, `1d`, or `1w`. Repeats are fixed elapsed-time intervals; if the bot is offline across several occurrences, it delivers once and advances to the next future occurrence rather than sending a burst of missed reminders. Omitting `repeat` keeps the existing one-shot behavior. Reminders are stored in `SUNFIRE_DATA_DIR/Reminders/reminders.json` (or `Data/Reminders/reminders.json` by default); existing reminder files without a repeat field continue to load as one-shot reminders. Delivery and persistence use at-least-once semantics if saving state fails after Discord accepts a reminder, so a reminder can be delivered again after a failed save or restart.

### Event-driven bot behavior

Bot event handling is bound in `NewBot()`:

- `onReady`
- `onInteractionCreate`
- `onGuildMemberAdd` / `Remove` / `Update`
- `onGuildBanAdd` / `Remove`
- `onGuildChannelCreate` / `Update` / `Delete`
- `onUserUpdate`
- `onMessageCreate` / `Update` / `Delete` / `DeleteBulk`

This means the bot acts as both a tool for slash-command execution and an event-observing automation layer for server changes and message lifecycle events.

### Moderation and access control

Access checks run in the interaction dispatcher before protected command handlers. This includes:

- public commands declared by `isPublicCommand()`
- `/automod` configuration is separately restricted to members with Manage Messages or Administrator permission
- AutoMod banned phrases and enabled state are persisted per guild under `Data/AutoMod/<guild-id>.json` (or `SUNFIRE_DATA_DIR/AutoMod/<guild-id>.json`); existing default rules are preserved
- AutoMod trigger history is persisted per guild and user under `Data/AutoModEvents/<guild-id>/<user-id>.jsonl`; `/automod view user:<member>` shows recent events to moderators
- owner-only command protection for other commands
- per-user grants for grantable restricted commands, persisted under `SUNFIRE_DATA_DIR/commandAccess.json`
- ticketing and privileged thread flows
- role menu actions
- moderation logging through Discord and file-backed state

AutoMod can be managed with `/automod banned-word-add`, `/automod banned-word-remove`, `/automod banned-word-list`, `/automod enable`, `/automod disable`, and `/automod status`. Banned phrases match case-insensitively as substrings; matching messages are deleted and recorded as warnings and trigger events. Trigger events include the timestamp, channel, matched phrase, message content (up to 500 characters), and whether deletion succeeded.

### AI and provider integrations

Sunfire integrates with multiple provider families through `providers/` and `venice/` modules.

Key system areas:

- `venice/` — chat, image, audio, transcription, queueing, and model APIs
- `/venice music` — generates songs with `minimax-music-v26` by default; accepts lyrics and a 120–360 second duration (default 240), quotes the cost before queueing, then retrieves and cleans up the completed audio
- `providers/` — provider abstraction layer for external APIs
- `openrouter.go` — OpenRouter-specific functionality
- `modules/` — reusable library patterns and helpers

The project is designed to support multiple generative and retrieval workflows without hardcoding a single backend.

The root `config.go` honors `SUNFIRE_CONFIG_FILE`. The `a2e/`, `providers/`, and `venice/` packages currently load configuration using their own repo-relative `.private/config.json` lookup. Deployments that enable these integrations must provision the shared config where those loaders expect it, or update those loaders to use the common config-path convention before relying only on an external config path.

### Background daemons

On startup, the bot launches several long-lived goroutines, including:

- `startPeriodicMediaDeduplication`
- `startReminderDaemon`
- `startHealthChecker`
- a set of notifier daemons for YouTube, Kick, Spotify, movies, freebies, scrape, birthdays, and Hermes tips

These daemons are registered together in `Bot.startWorkers()` and receive a shared cancellation context. Periodic jobs should use `runPeriodic()` so initial delays and tickers respond to cancellation. Do not add an untracked goroutine from `onReady()`; add it to the worker set and make its waits cancellable. Keep each worker safe to run once after Ready and tolerate a temporarily unavailable external service.

Announcement pollers validate successful upstream HTTP responses and only add an item to their delivered cache after Discord accepts the post (or, for Kick offline transitions, the edit). Failed sends and cache writes are logged; failed sends remain eligible on a later poll. If Discord accepts a message but the subsequent cache write fails, the next poll or process restart may send it again. This is at-least-once delivery, not exactly-once delivery.

Poller caches live under `SUNFIRE_DATA_DIR` (default `Data/`) and are persisted independently by service. Cache read, decode, and write errors are logged; malformed or inaccessible cache state can result in an already-delivered item being retried.

## Development workflow

### Build and run

```bash
cd /path/to/Sunfire
go test ./...
go build -o Sunfire .
BOT_TOKEN=your_token go run .
```

Run the bot from the repository root for local development so its default asset and `Data/` paths resolve as expected. Use `SUNFIRE_HOME`, `SUNFIRE_DATA_DIR`, and `SUNFIRE_CONFIG_FILE` when testing a packaged layout.

### Tests and static checks

```bash
go test -count=1 ./...  # all package tests, uncached
go test -race ./...     # detect data races in exercised code
go test -cover ./...    # statement coverage by package
go vet ./...            # Go static checks
```

Target a test by name or package when iterating:

```bash
go test . -run TestMessageEventHandlers -count=1
go test ./providers -run TestCommandSpecsCount -count=1
```

Prefer deterministic tests: use `t.TempDir()` for persistence, fixture HTTP transports for upstream calls, and the notifier send seams for Discord API outcomes. Do not make routine tests depend on live Discord, provider, or notifier services.

### Dependency model

The repository has one Go module, `sunfire`, declared in the root `go.mod`. The packages under `a2e/`, `providers/`, `venice/`, and `modules/` are part of that module and are exercised by `go test ./...` from the repository root. If adding a dependency, update `go.mod` and `go.sum`.

## Change workflow and conventions

### Adding a slash command

1. Define the Discord command and handler using the existing `commandSpec` pattern.
2. Add the command spec during `buildCommandSpecs()` or in the owning subsystem's `CommandSpecs()` provider.
3. Check `isPublicCommand()` and the existing access/grantability rules; new commands default to restricted unless explicitly listed public.
4. Add focused tests for command shape, access classification, and behavior. Avoid directly calling Discord APIs in logic that can be tested through a narrow seam.

### Adding state or persistence

1. Place application state under `dataPath(...)` so `SUNFIRE_DATA_DIR` is respected.
2. Use a temporary directory override in tests; do not read or write the developer's real `Data/`.
3. Return and log persistence errors instead of reporting success when a write failed.
4. Document whether state is reconstructible, backed up, retained indefinitely, or needs an explicit migration.

### Adding an external poller

1. Use a bounded-time HTTP client and validate status, body-read, and decode results before interpreting data.
2. Send to Discord before marking an item delivered in its persistent cache.
3. On send or cache failure, log enough context for operations and leave the item eligible for retry. A cache-write failure after successful delivery may produce duplicates (at-least-once semantics).
4. Register the cancellable worker in `Bot.startWorkers()` and test upstream errors, send failures, persistence errors, and successful deduplication with fixtures.

## Operational notes

### Message and media logging

The bot records message history and media-related activity for audit and follow-up use. This is handled by the message logging and media logger subsystems and stored under `SUNFIRE_DATA_DIR` (or `Data/` for local runs).

PostgreSQL message search and fetch only include messages received after a channel is enabled with `/message-log add`. New messages in enabled channels are archived automatically.

These logs are operationally significant and can contain user-provided text and attachments. Protect access to the data directory, include it in backups, monitor its disk use, and define an explicit retention policy appropriate to the server’s privacy requirements. The application does not automatically expire message or media logs. PostgreSQL message archives require their own database backup and retention policy.

### Logs

The Go runtime logger writes to standard output/error. Under systemd, these are captured by journald:

- `journalctl -u sunfire.service`
- `journalctl -u sunfire.service -f`

The optional Discord logging channel is a separate event stream, not a substitute for host logs. Configure journald retention/forwarding at the host level; the application does not configure file logging or rotate log files.

### Backups and restore

Back up the complete `SUNFIRE_DATA_DIR` and the configuration file. Back up PostgreSQL independently if message archiving is enabled. Schedule backups according to the service’s recovery objectives and verify restores periodically. For a consistent filesystem backup, stop the service first, or use a filesystem snapshot; copy files with ownership and restrictive permissions preserved. To restore, stop the service, restore state and config, restore the service account’s access, then start it and verify the journal and Discord connection. No automatic retention or pruning of message/media history is configured.

### Health and readiness

`systemctl is-active sunfire.service` reports process state only. It does not confirm the Discord websocket, command registration, external providers, or persistence backends are healthy. The optional `healthCheck` config probes the configured external URLs and reports transitions to Discord; it is not a local liveness/readiness HTTP endpoint. Check the service journal and Discord presence after deployment or restart. systemd restarts the process on exit, but an alive process with a degraded integration may require operator intervention.

## Linux systemd deployment

The repository includes [deploy/sunfire.service](./deploy/sunfire.service). The example assumes the release and assets are installed under `/opt/sunfire`, the config is `/etc/sunfire/config.json`, and persistent state is `/var/lib/sunfire`.

1. Build and install a release with the project’s Go toolchain:

   ```bash
   go test ./...
   go build -o Sunfire .
   sudo install -d -o root -g root -m 0755 /opt/sunfire
   sudo install -o root -g root -m 0755 Sunfire /opt/sunfire/Sunfire
   ```

   Copy required runtime assets/directories into `/opt/sunfire` as applicable.

2. Create the service account and runtime directories:

   ```bash
   sudo useradd --system --home-dir /var/lib/sunfire --shell /usr/sbin/nologin sunfire
   sudo install -d -o root -g sunfire -m 0750 /etc/sunfire
   sudo install -d -o sunfire -g sunfire -m 0750 /var/lib/sunfire
   ```

3. Create a populated `/etc/sunfire/config.json` containing the bot token, provider credentials, and server settings, then restrict it:

   ```bash
   sudoedit /etc/sunfire/config.json
   sudo chown sunfire:sunfire /etc/sunfire/config.json
   sudo chmod 0600 /etc/sunfire/config.json
   ```

   Do not copy production secrets into the repository.

4. Install and enable the unit:

   ```bash
   sudo install -o root -g root -m 0644 deploy/sunfire.service /etc/systemd/system/sunfire.service
   sudo systemctl daemon-reload
   sudo systemctl enable --now sunfire.service
   sudo systemctl status sunfire.service
   sudo journalctl -u sunfire.service -f
   ```

The unit runs as an unprivileged account, stores state in `/var/lib/sunfire`, applies systemd filesystem hardening, and restarts after process failure. It allows writes to `/etc/sunfire` because existing configuration-backed settings are persisted there; keep that directory and config file restricted to the service account and administrators. For upgrades, stop the unit, back up state/config, atomically replace the binary/assets, then start it and inspect the journal. Do not delete or replace `/var/lib/sunfire` during an upgrade.

## Security considerations

- Treat the configured config file and `SUNFIRE_DATA_DIR` as sensitive; never commit secrets or runtime message/media data.
- Do not expose bot tokens or provider keys in source control.
- Keep runtime logs scoped to local/private infrastructure unless intentionally redirected.
- Maintain awareness that the bot can operate on live server state and event streams.

## License

This repository does not clearly declare a root-level license file in the main tree. Confirm licensing requirements before public redistribution or external reuse.
