# File-Upload + Purge Commands (discordgo)

Patterns used by Sunfire (Go bot in `Repos/Enit`) for `/picture`, `/video`, and `/purge`.

## File-upload from a directory (`/picture`, `/video`)

- Scan directory with `os.ReadDir`; filter by extension (`.png`/`.jpg`/`.jpeg`/`.gif`/`.webp` for images; `.mp4`/`.mov`/`.avi`/`.mkv`/`.webm` for videos).
- Pick random with `rand.Intn(len(filtered))`.
- Send as file attachment: `discordgo.File{Name: chosen.Name(), Reader: f}` where `f` is `os.Open(path)`.
- The interaction reply must use `InteractionResponseChannelMessageWithSource` with `Files: []*discordgo.File{...}` and a content line naming the file.
- Directory paths are repo-relative and hardcoded in the handler (`/home/daniel/Repos/Enit/Pictures`, `/home/daniel/Repos/Enit/Videos`).

## Chunked bulk purge (`/purge`)

- Accept `amount` as a string option (`"all"` or a number 1–10000).
- Fetch messages in batches (`ChannelMessages(channelID, limit, beforeID)`) until the target count is reached or no more messages exist.
- Delete in chunks of 99 (Discord bulk-delete max). Try `ChannelMessagesBulkDelete` first; on error (message > 14 days old), fall back to `ChannelMessageDelete` per message in the chunk.
- For `"all"`: loop fetching until `len(msgs) == 0`; count all deleted.
- Defer the interaction (`InteractionResponseDeferredChannelMessageWithSource`) because bulk + single-delete over thousands of messages can exceed Discord's 3s window; respond via `FollowupMessageCreate`.
- Must be run inside a guild; `i.ChannelID` provides the target.

## Pitfalls

- `rand` needs `math/rand`; seed is not strictly required for this use case but deterministic if needed.
- File handles must stay open until the Discord session sends them; opening inline inside the handler is sufficient because the handler holds the reference through the interaction response.
- When filtering extensions, check `filepath.Ext()` — case-sensitive; the directories here use lowercase extensions.
- Bulk-delete error does NOT tell you WHICH message is too old — just that at least one in the chunk is >14 days. Falling back to single deletes for the entire chunk is safe but slightly slower; acceptable for an admin command.
