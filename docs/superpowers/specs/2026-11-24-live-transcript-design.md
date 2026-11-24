# Live transcript with the Claude voice stream

## Intent

Today a line shows up in the HUD 10 to 30 seconds after it was said, because oat sends a chunk only after the pause that ends it. The voice stream sends text while the audio arrives, and it corrects words as it hears more. The user wants to see that text in the HUD as it happens, with the corrections.

## Test results (2026-11-24)

| Test | Result |
|---|---|
| One connection, 13 min of audio sent at once | One endpoint, 4,889 characters. 40 short connections gave about 10,600 for the same audio, so the fast send lost text. |
| One connection, 8 min 44 s in real time, conversation engine on | Stayed open. 1,089 messages, one endpoint at the end, 7,149 characters. |
| The same, conversation engine off | Two messages, both at the end. No partial text. |
| Partial text in real time | Arrives 0.3 to 1 s after the speech. Each message has the full text of the utterance, with corrections. |

The server sends no endpoint during a real-time connection. A connection that stays open for the whole meeting therefore has no final text until it closes, and a failure loses all of it.

## Approach

Each speaker always has one stream open, but a stream lives for one chunk only. The chunker already cuts at pauses (10 to 30 s). While a chunk grows, its frames go to the stream in real time, and the HUD shows the partial text. At the cut, oat sends `CloseStream`, gets the final text, and writes it as segments. The next chunk uses a stream that oat opened in advance, so no audio waits for a connection.

Rejected:

- One stream for the whole meeting: no final text until the end, and one failure loses everything.
- The chunk replay of today only: safe, but the text comes 10 to 30 s late.

## Design

- `claudestt.Stream`: `Open(ctx, lang, onText)`, `Send(pcm)`, `Finish()`, `Close()`, `Alive()`. `onText` gets the full partial text after each message. A background KeepAlive every 8 s keeps an idle stream open. `Transcribe` becomes Open, Send all, Finish, so both paths share the code and the tests.
- `chunk.Chunker` gets an optional observer with three calls: a chunk starts (with the preroll frames), a frame joins the chunk, a chunk ends (and whether the next chunk continues at once). A cut can drop a chunk with less than 1 s of speech. The observer still sees that cut.
- `session` gets one live lane for each speaker:
  - A chunk start opens a live segment. It takes the spare stream, or opens one, and starts to open the next spare.
  - Frames go to the segment through a buffered channel, so the capture never waits for the network.
  - At the cut, the segment sends `CloseStream`. The chunk WAV is still written to disk first.
  - Success: oat writes the segments, places the sentences between the start and the end of the segment, and deletes the WAV (or keeps it with `keep_audio`).
  - Failure: the WAV goes to the replay queue of today. A crash leaves the WAV on disk, and the next start sends it.
  - Stop waits for the open segments before it closes the queue. Abandon cancels them, and their WAV files stay on disk.
- `LiveEvent{ID, Speaker, Start, Text, Done}` goes to the HUD. The HUD shows each open segment under the final lines, dim, and replaces its text on each event. `Done` removes it when the final segments arrive.
- Configuration: `live = true` by default. It works only with `transcriber = "claude"`. With `live = false`, oat uses the replay of today.

## Spike results that changed the design (2026-11-24)

| Test | Result | Change |
|---|---|---|
| 8 to 10 streams at the same time | Most closed with code 4029, "Rate limit exceeded" | The client keeps at most 4 streams open and maps 4029 to a rate limit of 5 s. |
| 2, 3, 4, and 6 streams at the same time | 2, 3, and 4 passed. With 6, one got 4029. | |
| 6 streams opened 2 s apart | The sixth got 4029 | The limit is on open streams, not on the rate of opens. |
| Burst of 1.5 s of buffered audio, then real time | The same text as real time | No spare stream. A chunk opens its stream after 0.5 s of speech and sends the held frames at once. |
| A 60 s stream against two streams of 30 s, cut in the middle of a sentence | 0.99 word similarity, no word lost or repeated | A hard cut can be at any frame. |
| A 2 min session, live against replay | 0.92 (Me) and 0.95 (Them) word similarity, 510 partial texts, no warning | |

## Limits

- With live on, the hard cut after 30 s of speech is at the last frame, not at the quietest frame of the last 2 s. The stream and the chunk file then hold the same audio, so a replay after a failure neither loses nor repeats words.
- The audio goes to the network twice only when a live stream fails.
- If the voice mode of Claude Code runs at the same time, the two share the limit of the server.

## Summary

- After the stop, oat runs `claude -p --safe-mode` with no tools, no MCP servers, and no session file. Safe mode keeps the CLAUDE.md files, hooks, and output styles of the user out of the notes. An older Claude Code without `--safe-mode` runs again without it.
- The summary runs as `oat summary --notify <id>` in its own session, so it ends even when the user quits oat.
- It refuses a meeting that a recording or a drain holds. It reads meta.json again before it changes the title.
