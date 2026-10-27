# ElyVenice modules — Venice AI Go client

A small, **stdlib-only** Go client library for the [Venice AI API](https://docs.venice.ai).
Module path: `github.com/daniel-kill/ElyVenice/modules`.

## Packages

| Package     | Endpoints                                             |
| ----------- | ----------------------------------------------------- |
| `venice`    | Root client: config, auth, HTTP plumbing, error types |
| `chat`      | `POST /chat/completions` (incl. streaming, `venice_parameters`, tools) |
| `image`     | `POST /image/generate`, `/image/edit`, `/image/multi-edit`, `/image/background-remove`, `/image/upscale`, `/image/styles`, `POST /images/generations` |
| `audio`     | `POST /audio/speech` (TTS), `/audio/transcriptions` (STT), `/audio/quote`, `/audio/queue`, `/audio/retrieve`, `/audio/complete` (music & SFX) |
| `video`     | `POST /video/quote`, `/video/queue`, `/video/retrieve`, `/video/complete` |
| `augment`   | `POST /augment/search` (web search), `/augment/scrape`, `/augment/text-parser` |
| `models`    | `GET /models`, `GET /models/traits`                    |
| `billing`   | `GET /billing/balance`, `/billing/usage-analytics`, `/billing/usage-history` |
| `characters`| `GET /characters`, `GET /characters/{slug}`, `GET /characters/{slug}/reviews` |
| `embeddings`| `POST /embeddings`                                       |

## API key resolution

`venice.NewClient()` resolves the bearer key in this order:

1. `venice.WithAPIKey(...)` option
2. `VENICE_ELYVENICE_API_KEY` environment variable
3. `VENICE_ELYVENICE_API_KEY` field of `~/.private/config.json`

No key → `NewClient` returns an error (it never panics).

## Quick start

```go
package main

import (
    "context"
    "fmt"

    "github.com/daniel-kill/ElyVenice/modules"
    "github.com/daniel-kill/ElyVenice/modules/chat"
)

func main() {
    ctx := context.Background()
    client, err := venice.NewClient() // key from ~/.private/config.json
    if err != nil {
        panic(err)
    }

    c := chat.NewClient(client)
    resp, err := c.Create(ctx, &chat.Request{
        Model: "venice-uncensored",
        Messages: []chat.Message{{Role: "user", Content: "Hello!"}},
    })
    if err != nil {
        panic(err)
    }
    fmt.Println(resp.Text())
}
```

### Streaming

```go
it, err := c.Stream(ctx, &chat.Request{Model: "venice-uncensored", Messages: ...})
// chunk-by-chunk:
for {
    chunk, err := it.Next()
    if errors.Is(err, io.EOF) { break }
    ...
}
// or accumulate the whole stream into one response:
acc, err := it.Accumulate()
fmt.Println(acc.Text())
```

### Web search (chat)

```go
req := &chat.Request{
    Model:    "venice-uncensored",
    Messages: []chat.Message{{Role: "user", Content: "Latest Go release?"}},
    VeniceParameters: &chat.VeniceParameters{
        EnableWebSearch:  "on",
        EnableWebCitations: true,
    },
}
```

### TTS / STT

```go
ac := audio.NewClient(client)
mp3, ct, err := ac.Speech(ctx, &audio.SpeechRequest{Model: "tts-kokoro", Input: "hi", Voice: "af_bella"})

trans, err := ac.Transcribe(ctx, &audio.TranscriptionRequest{
    Model: "nvidia/parakeet-tdt-0.6b-v3",
    File:  venice.MultipartFile{Name: "meeting.mp3", MIME: "audio/mpeg", Data: clip},
})
```

### Music / video (async queue)

```go
q, _ := ac.Quote(ctx, &audio.QuoteRequest{Model: "elevenlabs-music", DurationSeconds: &thirty})
job, _ := ac.Queue(ctx, &audio.QueueRequest{Model: "elevenlabs-music", Prompt: "warm strings", DurationSeconds: &thirty})
res, _ := ac.Wait(ctx, &audio.JobRef{Model: job.Model, QueueID: job.QueueID}, 5*time.Second)
// res.Data holds the finished audio
```

Video uses the same pattern via `video.NewClient(client)`. Private video models
return a `download_url` at queue time — fetch it with `vc.Download(ctx, url)`.

### Errors

Non-2xx responses return `*venice.APIError` with `StatusCode`, `Type`, `Code`,
`Message` and raw body. Inspect via `errors.As(err, &apiErr)`.

## Notes

- Stdlib only — no external dependencies (`go build ./...` needs no network).
- `billing.UsageHistory` is cursor-paginated; filters travel inside the cursor
  on continuation requests (`WalkUsageHistory` handles this).
- The legacy `GET /billing/usage` endpoint is sunset (410) and is not exposed.
