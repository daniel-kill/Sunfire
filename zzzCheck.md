# Sunfire — System Commands & Background Processes

> Comprehensive audit and reference guide of all registered Discord slash commands, background daemons, worker lifecycles, and event processors in the [Sunfire](file:///C:/Users/danie/VirtualDrive/Repos/GitHub/Sunfire) bot architecture.
>
> **Bot Owner:** Daniel Kill (`daniel@danielkill.online`)  
> **Primary Server:** `1557930395868209162`  
> **Runtime:** Go / `discordgo` with cancellable worker lifecycles  

---

## Table of Contents
- [1. Architectural Summary & Statistics](#1-architectural-summary--statistics)
- [2. Background Processes, Daemons & Workers](#2-background-processes-daemons--workers)
  - [2.1 Scheduled & Polling Daemons (`workerLifecycle`)](#21-scheduled--polling-daemons-workerlifecycle)
  - [2.2 Event-Driven Background Processors](#22-event-driven-background-processors)
  - [2.3 Worker Lifecycle Management](#23-worker-lifecycle-management)
- [3. Complete Slash Command Catalog](#3-complete-slash-command-catalog)
  - [A2E AI (1)](#a2e-ai)
  - [Cloudflare (1)](#cloudflare)
  - [Cohere (1)](#cohere)
  - [Core (System, Moderation, Media) (17)](#core-system-moderation-media)
  - [Groq (1)](#groq)
  - [Internet Archive (1)](#internet-archive)
  - [Media & Stream Notifiers (1)](#media-and-stream-notifiers)
  - [Mistral (1)](#mistral)
  - [Moderation & Staff (20)](#moderation-and-staff)
  - [NVIDIA (1)](#nvidia)
  - [OpenRouter (1)](#openrouter)
  - [Venice AI (1)](#venice-ai)
- [4. Data & State Storage Paths](#4-data--state-storage-paths)

---

## 1. Architectural Summary & Statistics

Sunfire provides **47 top-level slash commands** with hundreds of subcommands and options, integrated across **12 categories**. In addition, it orchestrates **11 independent background daemons** and **4 event-driven background processors** managed via a unified, graceful `workerLifecycle` context.

| Category | Top-Level Commands | Key Capabilities |
| :--- | :---: | :--- |
| **A2E AI** | 1 | `/a2e` |
| **Cloudflare** | 1 | `/cloudflare` |
| **Cohere** | 1 | `/cohere` |
| **Core (System, Moderation, Media)** | 17 | `/help`, `/userinfo`, `/media`, `/emojigg`, +13 more |
| **Groq** | 1 | `/groq` |
| **Internet Archive** | 1 | `/ia` |
| **Media & Stream Notifiers** | 1 | `/notifiers` |
| **Mistral** | 1 | `/mistral` |
| **Moderation & Staff** | 20 | `/purge`, `/setup`, `/dedup`, `/grant-access`, +16 more |
| **NVIDIA** | 1 | `/nvidia` |
| **OpenRouter** | 1 | `/openrouter` |
| **Venice AI** | 1 | `/venice` |

---

## 2. Background Processes, Daemons & Workers

### 2.1 Scheduled & Polling Daemons (`workerLifecycle`)

All daemons are initialized in `bot.go:startWorkers(s)` and bound to a cancellable root context. If a connection re-authenticates or reconnects, a lifecycle guard ensures workers are not spawned twice.

| Daemon / Process | Source File | Schedule / Interval | Primary Function |
| :--- | :--- | :--- | :--- |
| **Periodic Media Deduplication** | `media_logger.go` | Every 1 Hour (`time.Hour`) | Computes SHA-256 hashes of all logged media attachments in server media channels, pruning duplicates to prevent storage bloat. |
| **Reminder Scheduler** | `reminders.go` | Every 10 Seconds (`10 * time.Second`) | Scans `Data/reminders.json` for due reminders, sends notification messages or DMs to users, and clears completed reminders. |
| **Host & Gateway Health Checker** | `health_checker.go` | Continuous Ticker | Monitors Gateway ping latency, memory/goroutine metrics, host load, and posts heartbeats to the administrative logging channel. |
| **YouTube Video Notifier** | `notifier_youtube.go` | Periodic Polling (15m) | Polls YouTube RSS/API feeds for new channel uploads, validates cache to prevent duplicates, and posts embed announcements. |
| **Kick Stream Live Notifier** | `notifier_kick.go` | Periodic Polling | Checks live broadcast status for configured Kick channels and announces when streamers go live. |
| **Spotify Release Tracker** | `notifier_spotify.go` | Periodic Polling | Monitors Spotify artist profiles and playlists, announcing newly released singles, EPs, and albums. |
| **Movie Releases Tracker** | `notifier_movies.go` | Periodic Polling | Queries movie release feeds/scrapers and posts alerts for theatrical premieres and digital drops. |
| **Free Stuff & Game Deals** | `notifier_freestuff.go` | Periodic Polling | Tracks limited-time free game promotions across Epic Games Store, Steam, and GOG, alerting members to free claims. |
| **Target Image Scraper** | `notifier_scrape.go` | Periodic Polling | Automatically scrapes images from external image feeds, stores them to disk, and indexes them into the server media gallery. |
| **Birthday Announcer** | `birthdays.go` | Daily at Midnight (`America/New_York`) | Compares today's date against registered member birthdays in `Data/birthdays.json` and broadcasts celebratory announcements. |
| **Hermes Intelligence Tips** | `notifier_hermes.go` | Periodic Ticker | Periodically posts tips, prompt recommendations, and execution heuristics for Hermes and local/cloud reasoning LLMs. |

### 2.2 Event-Driven Background Processors

In addition to scheduled daemons, Sunfire runs real-time event processors attached to the `discordgo.Session` Gateway:

1. **Automod Real-time Threat Engine (`automod.go`):**
   - Triggers on every `MessageCreate` event.
   - Scans message text against banned words, custom regex rules, invite links, mass mentions, and spam frequency.
   - Automatically deletes offending messages, issues warnings, logs incidents, or applies timeouts.

2. **PostgreSQL Message Archive Queue (`message_log.go`):**
   - Batches incoming channel messages into an in-memory queue (`msgQueue`).
   - Flushes batches to PostgreSQL asynchronously using connection pooling (`pgPool`) with automatic 10-second exponential retry on failure (`messageLogRetryDelay`).

3. **Asynchronous Media Downloader & Archiver (`media_logger.go`):**
   - Spawns independent goroutines on message creation to stream attachments directly to local storage (`Media/`).
   - Computes SHA-256 hashes, writes metadata sidecars, and deduplicates identical assets.

4. **Interactive Game Engine & State Machines (`riddles.go`, `battle.go`, `fallen_star.go`):**
   - Stateful game loops managing timers, user answers, turn-based Sweet Dreams multiplayer combat, and branching CYOA text adventure states.

### 2.3 Worker Lifecycle Management

The bot implements a strict `workerLifecycle` structure (`service.go`):
- **Start:** Creates a cancellable context (`context.WithCancel`) and spawns all workers into a `sync.WaitGroup`.
- **Cancel:** On SIGINT/SIGTERM or bot shutdown, signals `cancelFunc()` to terminate all background loops immediately.
- **Wait:** Awaits worker completion with a 10-second hard timeout (`b.workers.wait(10 * time.Second)`), preventing leaked goroutines or corrupted data buffers.

---

## 3. Complete Slash Command Catalog

### A2E AI

#### `/a2e`
**Description:** A2E AI commands (image, video, tts, clone, tasks, etc.)

**Options / Subcommands:**
  - `image` `[subcommand]` *(Optional)*: Generate an image from text (default model: seedream)
      - `prompt` `[string]` **(Required)**: Image prompt
      - `model` `[string]` *(Optional)*: Image model (default from config)
      - `size` `[string]` *(Optional)*: Size e.g. 1024x1024
  - `video` `[subcommand]` *(Optional)*: Generate a video using Wan 3.0 (runs asynchronously)
      - `prompt` `[string]` **(Required)**: Video prompt
      - `model` `[string]` *(Optional)*: Video model (default from config)
      - `aspect_ratio` `[string]` *(Optional)*: 16:9 | 9:16 | 1:1
      - `duration` `[string]` *(Optional)*: Duration e.g. 5s
  - `tts` `[subcommand]` *(Optional)*: Generate speech using built-in A2E voices or your cloned voices
      - `text` `[string]` **(Required)**: Text to speak
      - `voice` `[string]` *(Optional)*: Voice id or cloned-voice name
  - `voice-clone` `[subcommand]` *(Optional)*: Clone a custom voice from an attached MP3 or audio URLs
      - `name` `[string]` **(Required)**: Name for the cloned voice
      - `mp3` `[attachment]` *(Optional)*: Audio sample to clone from (MP3/WAV/FLAC/M4A)
  - `voice-assign` `[subcommand]` *(Optional)*: Assign a friendly name to a raw voice ID (saves to local JSON)
      - `name` `[string]` **(Required)**: The friendly name to assign
      - `id` `[string]` **(Required)**: The raw A2E voice ID
  - `voices` `[subcommand]` *(Optional)*: List your custom cloned voices and A2E's built-in voice presets
  - `tasks` `[subcommand]` *(Optional)*: List recent asynchronous image and video generation tasks
  - `credits` `[subcommand]` *(Optional)*: Show remaining A2E account credits

---

### Cloudflare

#### `/cloudflare`
**Description:** Cloudflare Workers AI

**Options / Subcommands:**
  - `chat` `[subcommand]` *(Optional)*: Chat with Cloudflare Workers AI
      - `prompt` `[string]` **(Required)**: Your message
      - `model` `[string]` *(Optional)*: Model id (default @cf/meta/llama-3.3-70b-instruct-fp8-fast)
  - `image` `[subcommand]` *(Optional)*: Generate an image with Cloudflare Workers AI (flux-1-schnell)
      - `prompt` `[string]` **(Required)**: Image prompt
      - `model` `[string]` *(Optional)*: Model id (default @cf/black-forest-labs/flux-1-schnell)

---

### Cohere

#### `/cohere`
**Description:** Cohere AI Models

**Options / Subcommands:**
  - `chat` `[subcommand]` *(Optional)*: Chat with Cohere (command-a-plus-05-2026)
      - `prompt` `[string]` **(Required)**: Your message
      - `model` `[string]` *(Optional)*: Model id (default command-a-plus-05-2026)
  - `transcribe` `[subcommand]` *(Optional)*: Transcribe audio with Cohere (requires language)
      - `audio` `[attachment]` **(Required)**: Audio file to transcribe
      - `language` `[string]` **(Required)**: ISO-639-1 language code (REQUIRED, no auto-detect)
      - `model` `[string]` *(Optional)*: Model (default cohere-transcribe-03-2026)

---

### Core (System, Moderation, Media)

#### `/help`
**Description:** Browse commands by category or find one by name.

**Options / Subcommands:**
  - `category` `[string]` *(Optional)*: Browse a command category — *Choices: A2E AI (a2e), Internet Archive (archive), Cloudflare (cloudflare), Cohere (cohere), Core (System, Moderation, Media) (core), Groq (groq), Moderation & Staff (moderation), Mistral (mistral), Media & Stream Notifiers (notifiers), NVIDIA (nvidia), OpenRouter (openrouter), Overkill Archive (overkill), LLM Providers (providers), Venice AI (venice)*
  - `search` `[string]` *(Optional)*: Find commands whose names contain this text (letters, numbers, _ or -)

#### `/userinfo`
**Description:** Show available Discord profile, server, activity, and voice information.

**Options / Subcommands:**
  - `user` `[user]` *(Optional)*: The user to get info for

#### `/media`
**Description:** Apply an FFmpeg effect to an uploaded image, audio, or video file.

**Options / Subcommands:**
  - `file` `[attachment]` **(Required)**: Media file to transform

#### `/emojigg`
**Description:** Browse downloaded Emoji.gg assets and add them to the server.

**Options / Subcommands:**
  - `search` `[subcommand]` *(Optional)*: Find emoji assets by name or asset ID.
      - `query` `[string]` **(Required)**: Name or Emoji.gg asset ID to search for
      - `page` `[integer]` *(Optional)*: Page number when there are more than 15 matches
  - `list` `[subcommand]` *(Optional)*: Browse downloaded emoji assets by page.
      - `page` `[integer]` *(Optional)*: Page number (15 assets per page)
  - `random` `[subcommand]` *(Optional)*: Preview a random downloaded emoji.
  - `preview` `[subcommand]` *(Optional)*: Preview a downloaded emoji by name or asset ID.
      - `asset` `[string]` **(Required)**: Emoji name or Emoji.gg asset ID from search/list
  - `add` `[subcommand]` *(Optional)*: Add a downloaded emoji to the server (Manage Expressions required).
      - `asset` `[string]` **(Required)**: Emoji name or Emoji.gg asset ID from search/list
      - `name` `[string]` *(Optional)*: Optional server emoji name (letters, numbers, underscores)

#### `/sysinfo`
**Description:** Show live host system resources and uptime.

*No arguments or options required.*

#### `/remind`
**Description:** Add, list, and delete your reminders.

**Options / Subcommands:**
  - `add` `[subcommand]` *(Optional)*: Schedule a reminder.
      - `when` `[string]` **(Required)**: e.g. '10m', '1h', or '09/29/2026 at 14:30'
      - `message` `[string]` **(Required)**: What to remind you about
      - `repeat` `[string]` *(Optional)*: Optional interval, e.g. '1d' or '1w'
      - `timezone` `[string]` *(Optional)*: IANA timezone for local date/time (default: America/New_York)
  - `list` `[subcommand]` *(Optional)*: List your scheduled reminders.
  - `delete` `[subcommand]` *(Optional)*: Delete one of your reminders.
      - `id` `[string]` **(Required)**: Reminder ID shown by /remind list

#### `/analytics`
**Description:** Generate a chat activity heatmap and statistics.

*No arguments or options required.*

#### `/transcript`
**Description:** Export chat history to a file.

**Options / Subcommands:**
  - `limit` `[integer]` **(Required)**: Number of messages to export (max 5000)

#### `/serverstats`
**Description:** Display detailed server, channel, and media storage statistics.

*No arguments or options required.*

#### `/lyrics`
**Description:** Search Genius for song or album metadata and links.

**Options / Subcommands:**
  - `fetch-song` `[subcommand]` *(Optional)*: Search Genius for a song; returns metadata and a link, not lyrics.
      - `song` `[string]` **(Required)**: Song title, optionally with artist
  - `fetch-album` `[subcommand]` *(Optional)*: Search Genius for albums by an artist, optionally narrowed to an album.
      - `artist` `[string]` **(Required)**: Artist name
      - `album` `[string]` *(Optional)*: Optional album title

#### `/dice`
**Description:** Simulate dice odds: your win chance vs roll 0-100, 10,000,000 rolls.

**Options / Subcommands:**
  - `chance` `[string]` **(Required)**: Your win chance (2.00 to 98.00). Win when roll < chance.

#### `/pick`
**Description:** Pick random results from server data.

**Options / Subcommands:**
  - `random-member` `[subcommand]` *(Optional)*: Pick one or more random members from this server.
      - `count` `[integer]` *(Optional)*: How many members to pick (default: 1)
      - `include-bots` `[boolean]` *(Optional)*: Include bot accounts
      - `role` `[role]` *(Optional)*: Only pick members with this role
      - `exclude` `[string]` *(Optional)*: Comma-separated user IDs or mentions to exclude
      - `countdown-minutes` `[integer]` *(Optional)*: Optional countdown timestamp in minutes
  - `random-role` `[subcommand]` *(Optional)*: Pick one or more random roles from this server.
      - `count` `[integer]` *(Optional)*: How many roles to pick (default: 1)
      - `countdown-minutes` `[integer]` *(Optional)*: Optional countdown timestamp in minutes

#### `/riddle`
**Description:** Start or manage a multi-round riddle game in this channel.

**Options / Subcommands:**
  - `start` `[subcommand]` *(Optional)*: Start a new 10-round riddle game in this channel
  - `stop` `[subcommand]` *(Optional)*: Stop the active riddle game in this channel
  - `status` `[subcommand]` *(Optional)*: Show status of current riddle round

#### `/battle`
**Description:** Manage Sweet Dreams multiplayer battles.

**Options / Subcommands:**
  - `start` `[subcommand]` *(Optional)*: Start a Sweet Dreams battle!
  - `join` `[subcommand]` *(Optional)*: Join an open Sweet Dreams battle!

#### `/fallen-star`
**Description:** Play the Fallen Star dark fantasy CYOA text adventure game.

**Options / Subcommands:**
  - `start` `[subcommand]` *(Optional)*: Start a new Fallen Star adventure
  - `status` `[subcommand]` *(Optional)*: Resume or view current adventure status

#### `/birthday`
**Description:** Register your birthday and let staff view server birthdays.

**Options / Subcommands:**
  - `set` `[subcommand]` *(Optional)*: Set your birthday one time. This cannot be edited later.
      - `month` `[integer]` **(Required)**: Birth month (1-12)
      - `day` `[integer]` **(Required)**: Birth day (1-31)
  - `view` `[subcommand]` *(Optional)*: View birthdays registered in this server.
      - `month` `[integer]` *(Optional)*: Optional month filter (1-12)

#### `/translate`
**Description:** Translate text to a target language.

**Options / Subcommands:**
  - `text` `[string]` **(Required)**: Text to translate
  - `to` `[string]` **(Required)**: Target language code, e.g. en, es, fr, de, tl, ja

---

### Groq

#### `/groq`
**Description:** Groq LPU Inference Engine

**Options / Subcommands:**
  - `chat` `[subcommand]` *(Optional)*: Chat with Groq (openai/gpt-oss-20b)
      - `prompt` `[string]` **(Required)**: Your message
      - `model` `[string]` *(Optional)*: Model id (default openai/gpt-oss-20b)
  - `speak` `[subcommand]` *(Optional)*: Synthesize speech with Groq (orpheus)
      - `text` `[string]` **(Required)**: Text to speak
      - `voice` `[string]` *(Optional)*: autumn|diana|hannah|austin|daniel|troy (default autumn)
      - `model` `[string]` *(Optional)*: Model id (default canopylabs/orpheus-v1-english)
  - `transcribe` `[subcommand]` *(Optional)*: Transcribe audio with Groq (whisper-large-v3-turbo)
      - `audio` `[attachment]` **(Required)**: Audio file to transcribe
      - `language` `[string]` *(Optional)*: ISO-639-1 language code (optional)
      - `model` `[string]` *(Optional)*: Model (default whisper-large-v3-turbo)

---

### Internet Archive

#### `/ia`
**Description:** Search and explore the Internet Archive (search, item, download, wayback, stats, fts).

**Options / Subcommands:**
  - `search` `[subcommand]` *(Optional)*: Metadata search across the archive.
      - `query` `[string]` **(Required)**: Search query (e.g. space, creator:Heinlein).
      - `type` `[string]` *(Optional)*: Optional mediatype filter: audio, texts, movies, image, software, etree, web.
  - `item` `[subcommand]` *(Optional)*: Full metadata for an item by identifier.
      - `identifier` `[string]` **(Required)**: Item identifier (e.g. xfetch).
  - `download` `[subcommand]` *(Optional)*: Post a file from an item (auto-picks a small one if no file given).
      - `identifier` `[string]` **(Required)**: Item identifier.
      - `file` `[string]` *(Optional)*: Optional filename within the item (use /ia item to list files).
  - `wayback` `[subcommand]` *(Optional)*: Check if the Wayback Machine archived a URL.
      - `url` `[string]` **(Required)**: The URL to check (e.g. https://example.com).
  - `stats` `[subcommand]` *(Optional)*: View/download counts for an item.
      - `identifier` `[string]` **(Required)**: Item identifier.
  - `fts` `[subcommand]` *(Optional)*: Full-text search across the archive.
      - `query` `[string]` **(Required)**: Full-text search query.
  - `help` `[subcommand]` *(Optional)*: Show the /ia usage guide.

---

### Media & Stream Notifiers

#### `/notifiers`
**Description:** Show status of YouTube, Kick, Spotify, Movies, FreeStuff, & Scrape notifiers.

**Options / Subcommands:**
  - `status` `[subcommand]` *(Optional)*: Show status of YouTube, Kick, Spotify, Movies, FreeStuff, & Scrape notifiers.
  - `trigger` `[subcommand]` *(Optional)*: Force an immediate poll pass for a notifier.
      - `service` `[string]` **(Required)**: The service to trigger — *Choices: YouTube (youtube), Kick (kick), Spotify (spotify), Movies (TMDB) (movies), FreeStuff (GG.deals) (freestuff), Hermes Tips (hermes), Image Scrape (scrape)*

---

### Mistral

#### `/mistral`
**Description:** Chat with Mistral AI

**Options / Subcommands:**
  - `chat` `[subcommand]` *(Optional)*: Chat with Mistral (mistral-medium-latest)
      - `prompt` `[string]` **(Required)**: Your message
      - `model` `[string]` *(Optional)*: Model id (default mistral-medium-latest)

---

### Moderation & Staff

#### `/purge`
**Description:** Purge messages (amount: number or 'all', max 10000).

**Options / Subcommands:**
  - `amount` `[string]` **(Required)**: Number of messages to purge, or 'all'

#### `/setup`
**Description:** Create private Sunfire category/logs and read-only INFO/rules channels.

*No arguments or options required.*

#### `/dedup`
**Description:** Run SHA-256 duplicate media cleanup on server media logs.

*No arguments or options required.*

#### `/grant-access`
**Description:** Grant or remove a member's access to one restricted command.

**Options / Subcommands:**
  - `grant` `[subcommand]` *(Optional)*: Grant a member access to a restricted command.
      - `command` `[string]` **(Required)**: Restricted command name (without /)
      - `member` `[user]` **(Required)**: Member to grant access to
  - `remove` `[subcommand]` *(Optional)*: Remove a member's access to a restricted command.
      - `command` `[string]` **(Required)**: Restricted command name (without /)
      - `member` `[user]` **(Required)**: Member to remove access from

#### `/searchlogs`
**Description:** Search a user's logged chat and edit history.

**Options / Subcommands:**
  - `user` `[user]` **(Required)**: User to search
  - `keyword` `[string]` *(Optional)*: Keyword to filter by

#### `/warn`
**Description:** Issue a warning strike to a user.

**Options / Subcommands:**
  - `user` `[user]` **(Required)**: User to warn
  - `reason` `[string]` **(Required)**: Reason for warning

#### `/automod`
**Description:** Configure automatic moderation for banned words and phrases.

**Options / Subcommands:**
  - `banned-word-add` `[subcommand]` *(Optional)*: Add a word or phrase to the banned list.
      - `word` `[string]` **(Required)**: Word or phrase to block
  - `banned-word-remove` `[subcommand]` *(Optional)*: Remove a word or phrase from the banned list.
      - `word` `[string]` **(Required)**: Word or phrase to unblock
  - `banned-word-list` `[subcommand]` *(Optional)*: List the banned words and phrases.
  - `enable` `[subcommand]` *(Optional)*: Enable automatic moderation.
  - `disable` `[subcommand]` *(Optional)*: Disable automatic moderation.
  - `status` `[subcommand]` *(Optional)*: Show AutoMod's status and banned-word count.
  - `view` `[subcommand]` *(Optional)*: View a user's recent AutoMod trigger events.
      - `user` `[user]` **(Required)**: User whose AutoMod events to view

#### `/channel`
**Description:** Manage server channels.

**Options / Subcommands:**
  - `create` `[subcommand]` *(Optional)*: Create a new channel.
      - `type` `[string]` **(Required)**: The type of channel — *Choices: Text (text), Voice (voice), Category (category)*
      - `name` `[string]` **(Required)**: The channel name
      - `in` `[channel]` *(Optional)*: The parent category (ignored for category types)
  - `delete` `[subcommand]` *(Optional)*: Delete a channel.
      - `target` `[channel]` **(Required)**: The channel to delete
  - `rename` `[subcommand]` *(Optional)*: Rename a channel.
      - `target` `[channel]` **(Required)**: The channel to rename
      - `name` `[string]` **(Required)**: The new channel name
  - `lock` `[subcommand]` *(Optional)*: Prevent @everyone from sending messages in a channel.
      - `channel` `[channel]` **(Required)**: The channel to lock
  - `unlock` `[subcommand]` *(Optional)*: Allow @everyone to send messages in a channel.
      - `channel` `[channel]` **(Required)**: The channel to unlock

#### `/timeout`
**Description:** Timeout a user.

**Options / Subcommands:**
  - `user` `[user]` **(Required)**: User to timeout
  - `minutes` `[integer]` **(Required)**: Duration in minutes
  - `reason` `[string]` *(Optional)*: Reason for timeout

#### `/kick`
**Description:** Kick a user from the server.

**Options / Subcommands:**
  - `user` `[user]` **(Required)**: User to kick
  - `reason` `[string]` *(Optional)*: Reason for kick

#### `/ban`
**Description:** Add, remove, and view server bans.

**Options / Subcommands:**
  - `add` `[subcommand]` *(Optional)*: Ban a user from the server.
      - `user` `[user]` **(Required)**: User to ban
      - `days` `[integer]` *(Optional)*: Days of message history to delete (0-7)
      - `reason` `[string]` *(Optional)*: Reason for ban
  - `remove` `[subcommand]` *(Optional)*: Remove a user's ban.
      - `user` `[string]` **(Required)**: Banned user's Discord ID (shown by /ban view)
  - `view` `[subcommand]` *(Optional)*: List banned users.

#### `/slowmode`
**Description:** Set channel slowmode.

**Options / Subcommands:**
  - `seconds` `[integer]` **(Required)**: Slowmode delay in seconds (0 to disable)

#### `/backup`
**Description:** Export full server configuration to a JSON file.

*No arguments or options required.*

#### `/note`
**Description:** Manage private staff notes for users.

**Options / Subcommands:**
  - `add` `[subcommand]` *(Optional)*: Add a note for a user
      - `user` `[user]` **(Required)**: Target user
      - `text` `[string]` **(Required)**: Note content
  - `view` `[subcommand]` *(Optional)*: View recent notes for a user
      - `user` `[user]` **(Required)**: Target user
      - `limit` `[integer]` *(Optional)*: How many notes to show (default 10)
  - `delete` `[subcommand]` *(Optional)*: Delete a note by ID
      - `user` `[user]` **(Required)**: Target user
      - `noteid` `[integer]` **(Required)**: Note ID to delete

#### `/role`
**Description:** Add or remove roles from server members.

**Options / Subcommands:**
  - `add` `[subcommand]` *(Optional)*: Add a role to a member
      - `user` `[user]` **(Required)**: Target user
      - `role` `[role]` **(Required)**: Role to add
      - `reason` `[string]` *(Optional)*: Reason
  - `remove` `[subcommand]` *(Optional)*: Remove a role from a member
      - `user` `[user]` **(Required)**: Target user
      - `role` `[role]` **(Required)**: Role to remove
      - `reason` `[string]` *(Optional)*: Reason

#### `/rolemenu`
**Description:** Create a dropdown select menu for self-assigned roles.

**Options / Subcommands:**
  - `title` `[string]` **(Required)**: Header text for the role menu
  - `pairs` `[string]` **(Required)**: Semicolon separated list (Label:RoleName; Label:@Role)

#### `/autorole`
**Description:** Configure automatic role assignment for new server members.

**Options / Subcommands:**
  - `view` `[subcommand]` *(Optional)*: View current auto-role
  - `set` `[subcommand]` *(Optional)*: Set auto-role for new members
      - `role` `[role]` **(Required)**: Role to assign
  - `clear` `[subcommand]` *(Optional)*: Disable auto-role

#### `/ticket-panel`
**Description:** Post a button panel allowing members to create private support tickets.

*No arguments or options required.*

#### `/close-ticket`
**Description:** Close and archive the current support ticket thread.

*No arguments or options required.*

#### `/message-log`
**Description:** Manage which channels are logged to Postgres, and search logged messages.

**Options / Subcommands:**
  - `add` `[subcommand]` *(Optional)*: Start logging a text channel
      - `channel` `[channel]` **(Required)**: Text channel to add to message logging
  - `remove` `[subcommand]` *(Optional)*: Stop logging a text channel
      - `channel` `[channel]` **(Required)**: Text channel to remove from message logging
  - `list` `[subcommand]` *(Optional)*: Show channels currently being logged
  - `search` `[subcommand]` *(Optional)*: Search logged messages by text
      - `query` `[string]` **(Required)**: Text to search for
      - `channel` `[channel]` *(Optional)*: Restrict search to a channel
      - `user` `[user]` *(Optional)*: Restrict search to a user
      - `limit` `[integer]` *(Optional)*: Max results (1-25)
  - `stats` `[subcommand]` *(Optional)*: Show message logging stats (total messages, servers, channels)
  - `fetch` `[subcommand]` *(Optional)*: Return the full logged message as a .md file attachment
      - `messageid` `[string]` **(Required)**: Message ID to fetch (from a search result)

---

### NVIDIA

#### `/nvidia`
**Description:** Chat with NVIDIA NIM

**Options / Subcommands:**
  - `chat` `[subcommand]` *(Optional)*: Chat with NVIDIA (nemotron-3-super-120b)
      - `prompt` `[string]` **(Required)**: Your message
      - `model` `[string]` *(Optional)*: Model id (default nvidia/nemotron-3-super-120b-a12b)

---

### OpenRouter

#### `/openrouter`
**Description:** OpenRouter: browse models, prices, routing, retrieval, media, and benchmarks.

**Options / Subcommands:**
  - `models` `[subcommand]` *(Optional)*: Search the OpenRouter model catalog.
      - `query` `[string]` *(Optional)*: Keyword search (e.g. 'gemini', 'deepseek')
      - `sort` `[string]` *(Optional)*: Sort order — *Choices: Most popular (most-popular), Newest (newest), Price: low to high (pricing-low-to-high), Price: high to low (pricing-high-to-low), Context: high to low (context-high-to-low)*
      - `modality` `[string]` *(Optional)*: Filter by output modality — *Choices: Text (text), Image (image), Audio (audio), Video (video), Embeddings (embeddings)*
      - `max_price` `[number]` *(Optional)*: Max prompt price in USD per million tokens
      - `limit` `[integer]` *(Optional)*: How many models to show (max 20)
  - `deals` `[subcommand]` *(Optional)*: Show currently discounted models, ranked by discount.
      - `limit` `[integer]` *(Optional)*: How many to show (max 20)
  - `model` `[subcommand]` *(Optional)*: Full details for one model.
      - `slug` `[string]` **(Required)**: Model id, e.g. openai/gpt-5.6-sol
  - `compare` `[subcommand]` *(Optional)*: Compare two models side by side.
      - `model_a` `[string]` **(Required)**: First model id
      - `model_b` `[string]` **(Required)**: Second model id
  - `endpoints` `[subcommand]` *(Optional)*: List providers serving a model, with per-endpoint pricing and latency.
      - `slug` `[string]` **(Required)**: Model id
  - `providers` `[subcommand]` *(Optional)*: List all upstream OpenRouter providers.
  - `price` `[subcommand]` *(Optional)*: Show a model's price and estimate a cost.
      - `slug` `[string]` **(Required)**: Model id
      - `in_tokens` `[integer]` *(Optional)*: Input tokens to estimate (default 1000)
      - `out_tokens` `[integer]` *(Optional)*: Output tokens to estimate (default 500)
  - `key` `[subcommand]` *(Optional)*: Show live usage, limits, and free-model quota for your API key.
  - `gen` `[subcommand]` *(Optional)*: Audit one generation's cost and latency by id.
      - `id` `[string]` **(Required)**: Generation id, e.g. gen-1234567890
  - `analytics` `[subcommand]` *(Optional)*: Show request counts by model for recent days.
      - `days` `[integer]` *(Optional)*: Number of previous days to include (1-90, default 3)
  - `usage` `[subcommand]` *(Optional)*: Spend dashboard: today, week, month, lifetime.
  - `ask` `[subcommand]` *(Optional)*: Ask any OpenRouter model; the reply shows the real cost.
      - `prompt` `[string]` **(Required)**: Your prompt
      - `model` `[string]` *(Optional)*: Model id (default: openrouter/auto)
      - `max_tokens` `[integer]` *(Optional)*: Max output tokens (default 1024)
      - `temperature` `[number]` *(Optional)*: Sampling temperature (0-2)
  - `help` `[subcommand]` *(Optional)*: Show the /openrouter interactive usage guide.
  - `route` `[subcommand]` *(Optional)*: Route prompt across providers with sort preferences (price/latency/throughput).
      - `prompt` `[string]` *(Optional)*: Your prompt
      - `sort` `[string]` *(Optional)*: Provider priority sort — *Choices: Lowest price (price), Lowest latency (latency), Highest throughput (throughput)*
      - `model` `[string]` *(Optional)*: Model id (default: openrouter/auto)
  - `fallback` `[subcommand]` *(Optional)*: Failover prompt across an ordered list of backup models.
      - `prompt` `[string]` *(Optional)*: Your prompt
      - `models` `[string]` *(Optional)*: Comma-separated model IDs in order of priority
  - `free` `[subcommand]` *(Optional)*: Chat via the zero-cost models router (openrouter/free).
      - `prompt` `[string]` **(Required)**: Your prompt
      - `max_tokens` `[integer]` *(Optional)*: Max output tokens (default 1024)
  - `online` `[subcommand]` *(Optional)*: Chat with web search-augmented answers (:online).
      - `prompt` `[string]` **(Required)**: Your query
      - `model` `[string]` *(Optional)*: Model id (default: openrouter/auto)
      - `max_tokens` `[integer]` *(Optional)*: Max output tokens (default 1024)
  - `think` `[subcommand]` *(Optional)*: Chat with reasoning/thinking models with explicit reasoning budget.
      - `prompt` `[string]` **(Required)**: Your reasoning prompt
      - `model` `[string]` *(Optional)*: Model id (default: openrouter/auto)
      - `reasoning_tokens` `[integer]` *(Optional)*: Max reasoning tokens (default 2048)
      - `max_tokens` `[integer]` *(Optional)*: Max output tokens (default 2048)
  - `tools` `[subcommand]` *(Optional)*: List models that support native function and tool calling.
  - `embed` `[subcommand]` *(Optional)*: Generate vector embeddings for input text.
      - `input` `[string]` **(Required)**: Text to generate embedding for
      - `model` `[string]` *(Optional)*: Embedding model (default: openai/text-embedding-3-small)
      - `dimensions` `[integer]` *(Optional)*: Target dimensions (optional)
  - `rerank` `[subcommand]` *(Optional)*: Rerank candidate documents against a query by relevance score.
      - `query` `[string]` **(Required)**: Search query
      - `documents` `[string]` **(Required)**: Candidate documents separated by |
      - `model` `[string]` *(Optional)*: Rerank model (default: cohere/rerank-v3.5)
      - `top_n` `[integer]` *(Optional)*: Number of top results to return (default 5)
  - `image` `[subcommand]` *(Optional)*: Generate an image via OpenRouter image generation models.
      - `prompt` `[string]` **(Required)**: Image description prompt
      - `model` `[string]` *(Optional)*: Image model (default: bytedance-seed/seedream-5-0-flash)
  - `video` `[subcommand]` *(Optional)*: Generate video or browse video models in the catalog.
      - `prompt` `[string]` *(Optional)*: Video description prompt
      - `model` `[string]` *(Optional)*: Video model (default: luma/ray-2)
  - `tts` `[subcommand]` *(Optional)*: Convert text to speech audio (.mp3 attachment).
      - `text` `[string]` **(Required)**: Text to speak
      - `model` `[string]` *(Optional)*: TTS model (default: hexgrad/kokoro-82m)
      - `voice` `[string]` *(Optional)*: Voice ID (default: af_bella)
  - `stt` `[subcommand]` *(Optional)*: Transcribe speech audio file or URL to text.
      - `audio` `[attachment]` *(Optional)*: Upload audio file attachment
      - `audio_url` `[string]` *(Optional)*: Direct audio URL to transcribe
      - `model` `[string]` *(Optional)*: STT model (default: openai/whisper-large-v3)
      - `language` `[string]` *(Optional)*: Spoken language code (e.g. en, es, fr)

---

### Venice AI

#### `/venice`
**Description:** Venice AI commands (chat, image, tts, music, search, etc.)

**Options / Subcommands:**
  - `chat` `[subcommand]` *(Optional)*: Chat with a Venice text model
      - `prompt` `[string]` **(Required)**: Your message
      - `model` `[string]` *(Optional)*: Model id (default from config)
      - `web` `[boolean]` *(Optional)*: Enable web search
  - `image` `[subcommand]` *(Optional)*: Generate an image with Venice
      - `prompt` `[string]` **(Required)**: Image prompt
      - `model` `[string]` *(Optional)*: Image model (default flux-2-pro)
      - `size` `[string]` *(Optional)*: Size e.g. 1024x1024
  - `tts` `[subcommand]` *(Optional)*: Generate speech audio (text-to-speech)
      - `text` `[string]` **(Required)**: Text to speak
      - `voice` `[string]` *(Optional)*: Voice id or cloned-voice name (default from config)
  - `voice-clone` `[subcommand]` *(Optional)*: Clone a voice from an attached audio sample (Chatterbox HD)
      - `name` `[string]` **(Required)**: Name for the cloned voice
      - `mp3` `[attachment]` *(Optional)*: Audio sample to clone from (MP3/WAV/FLAC/M4A)
      - `model` `[string]` *(Optional)*: tts-chatterbox-hd (default) | tts-minimax-speech-02-hd
  - `voices` `[subcommand]` *(Optional)*: List your cloned Venice voices
  - `balance` `[subcommand]` *(Optional)*: Show Venice account balance
  - `models` `[subcommand]` *(Optional)*: List Venice models (by modality)
      - `type` `[string]` **(Required)**: text|image|video|music|embedding|asr
  - `music` `[subcommand]` *(Optional)*: Generate a song with Venice (120-360 sec, cost quoted first)
      - `prompt` `[string]` **(Required)**: Style/genre prompt
      - `lyrics` `[string]` *(Optional)*: Lyrics text
      - `duration` `[integer]` *(Optional)*: Song length in seconds (120-360, default 240)
  - `stt` `[subcommand]` *(Optional)*: Transcribe an attached audio file (speech-to-text)
      - `audio` `[attachment]` **(Required)**: Audio file to transcribe
      - `model` `[string]` *(Optional)*: ASR model (default openai/whisper-large-v3)
      - `language` `[string]` *(Optional)*: Optional language code (e.g. en)
  - `search` `[subcommand]` *(Optional)*: Web search via Venice augment
      - `query` `[string]` **(Required)**: Search query
      - `max_results` `[string]` *(Optional)*: Max results (default 5)
      - `provider` `[string]` *(Optional)*: brave (default) | google
  - `scrape` `[subcommand]` *(Optional)*: Scrape a URL to markdown via Venice augment
      - `url` `[string]` **(Required)**: URL to scrape
  - `video` `[subcommand]` *(Optional)*: Generate a video via Venice (async)
      - `prompt` `[string]` **(Required)**: Video prompt
      - `model` `[string]` *(Optional)*: Video model (default seedance-2-5-text-to-video-basic)
      - `aspect_ratio` `[string]` *(Optional)*: 21:9 | 16:9 | 4:3 | 1:1 | 3:4 | 9:16 (default 16:9)
      - `resolution` `[string]` *(Optional)*: 1080p | 720p | 480p (default 1080p)
      - `duration` `[string]` *(Optional)*: Duration e.g. 5s
  - `embed` `[subcommand]` *(Optional)*: Generate an embedding for text
      - `input` `[string]` **(Required)**: Text to embed
      - `model` `[string]` *(Optional)*: Embedding model (default text-embedding-bge-m3)
  - `characters` `[subcommand]` *(Optional)*: List Venice characters
      - `search` `[string]` *(Optional)*: Optional search term
  - `upscale` `[subcommand]` *(Optional)*: Upscale an image via Venice
      - `image` `[attachment]` **(Required)**: Image to upscale
      - `model` `[string]` *(Optional)*: Upscale model
      - `scale` `[string]` *(Optional)*: Scale factor 2-8

---

## 4. Data & State Storage Paths

| Directory / File | Description |
| :--- | :--- |
| `Data/` | Active runtime state JSON files (`reminders.json`, `birthdays.json`, `messageLogChannels.json`, etc.) |
| `.private/` | Private configuration and credentials (`config.json`, sensitive tokens) |
| `.logs/` | Service execution logs (`service.log`, `service_error.log`, `push.sh.log`) |
| `Media/` | Downloaded server media archives, images, videos, and deduplicated assets |
| `deploy/` | Deployment manifests and systemd unit (`sunfire.service`) |
