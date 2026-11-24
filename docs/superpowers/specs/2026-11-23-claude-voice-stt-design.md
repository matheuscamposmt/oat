# Transcription with the Claude Code voice stream

## Intent

oat sends the audio chunks to Groq today. The user wants to stop that for two reasons:

- Privacy. The audio goes to one company less.
- Cost. The Claude subscription already pays for the speech-to-text that the Claude Code voice mode uses.

## The endpoint

The Claude Code voice mode (version 2.1.330) uses a private WebSocket. Anthropic does not document it.

- URL: `wss://api.anthropic.com/api/ws/speech_to_text/voice_stream`
- Query: `encoding=linear16`, `sample_rate=16000`, `channels=1`, `endpointing_ms=300`, `utterance_end_ms=1000`, `use_conversation_engine=true`, and `language`.
- Headers: `Authorization: Bearer <claude.ai OAuth access token>`, `x-app: cli`, `anthropic-client-platform: cli`, and a User-Agent.
- The client sends `{"type":"KeepAlive"}`, binary PCM frames, then `{"type":"CloseStream"}`.
- The server sends `TranscriptInterim` and `TranscriptText` (the full text of the current utterance, which grows), `TranscriptEndpoint` (the utterance is final), `TranscriptError`, and `error`.

The token comes from `claudeAiOauth.accessToken` in `~/.claude/.credentials.json`.

## Spike results (2026-11-23)

| Test | Result |
|---|---|
| Portuguese, `language=pt` | Good quality |
| Two streams at the same time | Both work |
| Send 20 s of audio in 0.3 s | Works. The final text comes about 2 s after `CloseStream`. |
| User-Agent `oat/dev` | Accepted |
| `language=auto` or `language=multi` | Refused with close code 1003 |
| No `language` parameter | Works, and detects Portuguese |
| `language=en` on Portuguese audio | Still gives Portuguese text |
| The server after the last `TranscriptEndpoint` | Closes the socket about 5 s later |

One quirk: the last `TranscriptText` before an endpoint can be shorter than the one before it. It can drop the last words when the chunk ends in the middle of a sentence. Claude Code itself loses these words.

## Goals

- `transcriber = "claude"` is the default. oat needs no Groq key in that case.
- `transcriber = "groq"` keeps the old path, as a fallback for when the private endpoint breaks.
- The chunk pipeline, the crash recovery, and the drain of leftover chunks stay as they are.

## Non-goals

- Live streaming of the mic for lower latency. The chunks already give a line 10 to 30 s after speech, and file replay keeps the crash recovery.
- A refresh of the OAuth token by oat.

## Anti-goals

- oat never writes `~/.claude/.credentials.json`. A refresh by oat rotates the refresh token and can log Claude Code out.
- oat never prints or logs the token.

## Approaches

1. Replay each chunk file over a new WebSocket, as fast as the socket takes it. Chosen. It fits the `transcribe.Client` interface, so the queue, the retries, the `failed/` folder, and the drain do not change.
2. One long stream per speaker, live, during the whole meeting. Rejected. It needs a new pipeline, a crash loses the audio in flight, and nothing is known about the limits for a stream of one hour.
3. Remove Groq. Rejected for now. The endpoint is private and can change in any Claude Code release, so a fallback stays.

## Design

- A new package `internal/stt` holds the types that both backends share: `Options`, `Result`, `Segment`, and the errors `AuthError`, `RateLimitError`, `TemporaryError`, `RequestError`. The package `groq` keeps type aliases, so its callers do not change.
- A new package `internal/claudestt` has a `Client` with `Transcribe(ctx, wav, opts)`:
  - It reads the token from the credentials file on each call, so a refresh by Claude Code takes effect without a restart of oat.
  - A missing token gives an `AuthError`, which stops the pipeline. An expired token gives a `TemporaryError`: the pipeline waits with backoff, and reads the file again on each try, so a renewal by Claude Code during a meeting lets oat continue.
  - It sends the PCM after the 44-byte WAV header in frames of 100 ms, without pacing, then `CloseStream`.
  - It keeps the last text of each utterance. If the last text is a shorter prefix of an earlier text in the same utterance, it keeps the longer one.
  - After `CloseStream`, it ends on the first `TranscriptEndpoint` plus 1 s of silence from the server, on a close by the server, or after 20 s.
  - `language=auto` omits the parameter. `opts.Prompt` and `opts.Model` are not used.
- Timestamps: the endpoint gives none, so oat sets them. oat splits each utterance into sentences and gives each sentence a part of the chunk in proportion to its length. Sentences keep the segments short, as with Whisper, so the echo filter in `store.Clean` compares segments of a few seconds and not of 30 seconds.
- A chunk with no speech gets no text, and the server closes the stream after about 1.8 s. The client returns an empty result and no error. The idle wait of 5 s is only a guard.
- In all the live tests, the stream ended when the server closed it, about 1 to 2 s after `CloseStream`. A comparison of 24 chunks with a client that waits 20 s for more messages gave the same text for each chunk.
- Each call has a timeout of 90 s. After it, the error is temporary and the pipeline tries again.
- Privacy: an error from the server named a "harmonic provider". Anthropic does not document which service transcribes the audio.
- Errors: an HTTP 401 or 403 on the upgrade gives `AuthError`. HTTP 429 gives `RateLimitError`. Close code 1003 or `TranscriptError` gives `RequestError`. A network error or HTTP 5xx gives `TemporaryError`.
- Configuration: a new key `transcriber` (`claude` or `groq`) and a flag `--transcriber`. The pipeline messages use the name of the backend in place of "Groq".
- `oat doctor`: for `claude`, it tests that the credentials file has a token, shows the minutes until it expires, and opens and closes one stream. For `groq`, it does the tests of today.
- The README describes the new default, the risk of a private endpoint, and the Groq fallback.

## Test results (2026-11-23)

- Go client against the real endpoint: speech of 30 s, speech of 1.5 s, 10 s of silence, `auto`, and `Ping` pass.
- 40 chunks of 18 s with 2 workers: 40 passed, no rate limit, about 3 s for each chunk, 67 s in total. The limits for a long meeting are still unknown.
- Drain of 3 chunks through the pipeline into `transcript.md`: passes in 6 s.
- Echo filter: the same audio as Me and Them, with the chunk borders 7 s apart. `store.Clean` drops 10 of the 12 Me segments. The 2 that stay have fewer than 3 words, which the filter skips by design.
- Silence of 1.5 s at the end of a chunk, or `use_conversation_engine=false`, did not change the text on 4 chunks. The client keeps neither.

## Left to the builder

- The name of the HUD messages and of the doctor lines.
- The WebSocket library: `github.com/coder/websocket`.
