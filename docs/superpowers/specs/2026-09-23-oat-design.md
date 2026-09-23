# oat: design

Date: 2026-09-23. Status: approved in brainstorming, waiting for spec review.

## Purpose

oat records meetings on Linux from the terminal. It captures the microphone and the system audio as two separate streams. It sends the audio to the Groq Whisper API and saves the transcript next to the notes that you type during the meeting. Claude Code reads the meetings through an MCP server that is part of the same binary.

The model for the product is Granola. You type short notes, the app keeps the transcript, and an AI turns both into meeting notes. In oat, Claude does that last step through MCP.

## Goals and non-goals

Goals:

1. Run from the terminal only, with a HUD in the style of Claude Code.
2. Transcribe with the Groq cloud API. oat runs no local speech model.
3. Read the Groq key from the `env` blocks of `~/.claude/settings.json` and `~/.claude/settings.local.json`.
4. Keep the microphone ("Me") and the system audio ("Them") as streams that never mix.
5. Give Claude read access to all meetings through MCP.
6. Install with one command, as one static binary with no runtime.

Non-goals:

1. Speaker separation inside the "Them" stream. All remote voices are "Them".
2. Calls to an LLM from oat. Claude writes the summaries through MCP.
3. macOS, Windows, or a Linux system without PulseAudio or PipeWire with its Pulse layer.
4. A GUI, a tray icon, or automatic detection of the start of a meeting.
5. Audio playback.

## Decisions from brainstorming

| Topic | Decision |
|---|---|
| HUD during a meeting | Live transcript and a notepad, side by side |
| Echo | `auto` mode: echo cancel on speakers, off on headphones, text filter always on |
| Language | `pt` by default. The `--lang` flag and `ctrl+l` change it |
| Stack | Go with Bubble Tea v2, Lip Gloss v2, Bubbles v2, and the official MCP Go SDK |
| Location | `~/oat` (the folder `~/projetos` belongs to root) |

## Architecture

oat is one binary with five subcommands. The HUD and the MCP server share no memory. They share the meeting folders on disk.

```
                 ┌──────────────── oat (HUD) ────────────────────┐
 mic ────parec──▶│ capture "me"   ─▶ chunker ─┐                  │
                 │                            ├─▶ Groq workers ─▶│─▶ ~/.local/share/oat/meetings/<id>/
 monitor─parec──▶│ capture "them" ─▶ chunker ─┘                  │      meta.json  transcript.jsonl
                 │ Bubble Tea HUD ◀── events                     │      notes.md   transcript.md
                 └───────────────────────────────────────────────┘
 claude ─stdio─▶ oat mcp ──reads files──▶ ~/.local/share/oat/meetings/
```

| Command | Behavior |
|---|---|
| `oat` | Opens the home screen with the list of meetings |
| `oat new [title] [--lang xx]` | Starts a recording at once. The flags can come before or after the title |
| `oat mcp` | Runs the MCP server on stdio. Claude Code starts it |
| `oat doctor` | Tests the environment and prints a fix for each failure |
| `oat setup` | Registers the MCP server in Claude Code |

### Packages

Each package has one job and a small interface. The packages at the bottom of the table do not import the packages above them.

| Package | Job | Interface |
|---|---|---|
| `cmd/oat` | Parses the subcommands and flags | `main` |
| `internal/tui` | Bubble Tea models for home, recording, and viewer, plus the theme | Consumes `session` events |
| `internal/mcpserver` | MCP tools and the `enhance` prompt | `Run(ctx, store)` |
| `internal/doctor` | Environment tests | `Run(ctx) []Result` |
| `internal/session` | One recording. Connects audio, chunker, transcriber, and store | `Start(opts)`, `Events()`, `Pause`, `Resume`, `SetLang`, `Stop` |
| `internal/transcribe` | Queue, workers, retry, segment filters | `Enqueue(chunkFile)`, emits segments |
| `internal/groq` | HTTP client for the transcription endpoint | `Transcribe(ctx, wav, opts) (Result, error)` |
| `internal/chunk` | VAD and chunk cuts, WAV encoding. Pure code, no I/O | `Push(frame) (*Chunk, bool)`, `Flush()` |
| `internal/audio` | `parec` capture, device lookup, echo-cancel module | `Open(ctx, device)` gives 20 ms frames on a channel |
| `internal/store` | Meeting folders, metadata, segments, notes, rendering, search | `Create`, `List`, `Load`, `AppendSegments`, `WriteNotes`, `Clean`, `Search` |
| `internal/config` | Configuration file, flags, Groq key lookup | `Load(flags) (Config, error)` |

## Audio capture

oat runs two `parec` child processes. Each one gives raw PCM: 16 kHz, mono, signed 16-bit little endian. The Pulse server does the resampling. oat uses no cgo, so the binary stays static.

```
parec --device=<device> --format=s16le --rate=16000 --channels=1 --raw --latency-msec=100
```

| Stream | Device |
|---|---|
| Me | The default source from `pactl get-default-source`. With echo cancel on, `oat_ec_source` |
| Them | `<default sink>.monitor`, with the sink name from `pactl get-default-sink` |

oat resolves the device names at start and passes explicit names to `parec`. The "Them" stream always reads the monitor of the real sink, so the echo-cancel sink does not change what it hears.

Both streams use one meeting clock. The time of a frame is its index in the stream multiplied by 20 ms. At a pause, the chunker flushes the current chunk. During the pause, oat reads and discards the frames, and the clock continues. Timestamps in the transcript therefore match the wall clock.

### Device changes

oat runs `pactl get-default-sink` and `pactl get-default-source` every 2 seconds. If a default device changes, oat stops the matching `parec` process and starts a new one on the new device. The meeting clock continues. oat itself sets the echo-cancel sink as the default. oat ignores that change. After a change of the output device, oat evaluates the echo mode again. For example, a Bluetooth headset that connects during the call turns echo cancel off.

With echo cancel on, a new default sink means that you chose a new output. oat then unloads the module but keeps your new sink as the default. It starts the "Me" stream again on the default source. If the new sink is not headphones, oat loads the module again with the new sink as `sink_master`.

If `parec` stops for another reason, oat starts it again, up to 3 times in 10 seconds. After that, the HUD shows a warning, and the meeting continues with the other stream.

### Echo

On speakers, the microphone also records the remote voices. The transcript then shows their words twice. oat uses two layers against this.

The first layer is the PipeWire echo-cancel module. The configuration key `echo` has three values: `auto`, `on`, and `off`. In `auto` mode, oat reads the default sink in `pactl list sinks`. The output counts as headphones in these cases:

1. The active port name contains "headphone" or "headset", in any case.
2. The property `device.bus` is `bluetooth`.
3. The property `device.form_factor` is `headphone` or `headset`.

For headphones, echo cancel stays off. For all other outputs, including HDMI, echo cancel is on. To turn it on, oat does these steps:

1. Save the name of the current default sink.
2. Run `pactl load-module module-echo-cancel aec_method=webrtc source_master=<source> sink_master=<sink> source_name=oat_ec_source sink_name=oat_ec_sink`, and keep the module index that it prints.
3. Run `pactl set-default-sink oat_ec_sink`, so that the call audio goes through the module and gives it a reference signal.
4. Write the module index and the saved sink to `~/.local/state/oat/echo.json`.
5. Record the "Me" stream from `oat_ec_source`.

To turn it off, oat sets the saved sink as the default again, unloads the module, and deletes `echo.json`. oat does this at stop, on `SIGINT`, `SIGTERM`, and `SIGHUP`, and after a device change to headphones. If a crash leaves `echo.json` behind, the next start of oat (except `oat mcp`) and `oat doctor` do the same cleanup. Before they unload the module, they make sure that its index still belongs to `module-echo-cancel` with the sink name `oat_ec_sink`.

If the module does not load, oat records without it and shows a yellow warning in the HUD.

The second layer is a text filter. For each "Me" segment, it tests three conditions. If all three are true, it drops the segment:

1. The segment has 3 words or more.
2. A "Them" segment overlaps it in time, within 5 seconds.
3. 70% or more of its words, in lowercase and without punctuation, also appear in that "Them" segment.

The filter runs in `store.Clean` at read time, not at write time. Segments from the two streams arrive in any order, and `Clean` sees both. The raw segments stay in `transcript.jsonl`.

## Chunking

The chunker splits each stream into chunks for the Groq API. It works on 20 ms frames of 320 samples. It is pure code, so the tests feed it synthetic PCM.

The VAD (voice activity detection) is based on energy. For each frame, the chunker computes the RMS level in dBFS. The noise floor is the lowest frame level in the last 5 seconds. A speech frame has a level of 12 dB or more above the noise floor, and a level above -50 dBFS. A hangover of 200 ms joins speech that has short gaps.

| Rule | Value |
|---|---|
| Start of a chunk | The first speech frame, plus 300 ms of audio before it from a ring buffer |
| Normal cut | The chunk is 10 s or longer and a silence reaches 700 ms. The cut is in the middle of the silence |
| Early cut | A silence reaches 3 s. The chunk ends 300 ms after the last speech, at any length |
| Hard cut | The chunk reaches 30 s. The cut is at the quietest frame of the last 2 s |
| Drop | The chunk has less than 1 s of speech in total |
| Stop | The chunker flushes the current chunk |

Groq bills 10 seconds minimum per request, so the normal cut waits for 10 seconds of audio. The early cut shows a short reply in the HUD without a long delay. All values are constants in one file, because they need tuning on real audio.

A chunk has a speaker, a start time on the meeting clock, the samples, and the seconds of speech. The session writes it to disk as a WAV file of 16 kHz mono PCM. A 30-second chunk is about 960 KB.

## Transcription

The session writes each chunk to `<meeting>/chunks/<speaker>-<start ms>.wav` before it goes to the queue. The file name holds the speaker and the start time, so oat can put leftover files back in the queue with no other state.

Two workers send the chunks to Groq:

```
POST https://api.groq.com/openai/v1/audio/transcriptions
file=<chunk.wav>  model=whisper-large-v3-turbo  language=pt
response_format=verbose_json  temperature=0  prompt=<last 30 words of the same speaker>
```

The prompt helps Whisper keep names and terms across chunks. A worker reads the current language at the time that it sends a chunk. If the language is `auto`, oat does not send the `language` field. `ctrl+l` also writes the new language to `meta.json`, so a later drain uses it.

oat adds the chunk start time to the segment times that Groq returns. It drops a segment in these cases:

1. `no_speech_prob` is more than 0.6.
2. `compression_ratio` is more than 2.4, which is the sign of a repetition loop.
3. The normalized text is on the list of known Whisper hallucinations, for example "Legendas pela comunidade Amara.org".

The workers append the other segments to `transcript.jsonl` and send them to the HUD.

| Groq response | Behavior |
|---|---|
| 200 | Save the segments. Delete the WAV file. With `keep_audio`, move it to `audio/` |
| 429 | Wait for the seconds in `retry-after`. Without the header, wait 10 s. Send again |
| 5xx or network error | Retry with backoff: 2, 4, 8, 16, 32, then 60 s for each retry after that |
| 401 | Stop the workers. Keep the chunks on disk. The HUD shows a red banner: "Groq rejected the API key" |
| Other 4xx | Move the file to `<meeting>/failed/`. The HUD shows a warning |

The HUD shows the number of chunks in the queue. When you stop a recording, the queue drains while the HUD shows the progress. If you quit before the queue is empty, the next start of the HUD, with `oat` or `oat new`, drains it in the background.

The Groq free tier limits the audio seconds per hour. Two streams can send up to twice the length of the meeting. The drop rule for chunks with little speech keeps the total lower.

## Storage

oat follows the XDG base directories:

| Path | Content |
|---|---|
| `$XDG_DATA_HOME/oat/meetings/` (default `~/.local/share/oat/meetings/`) | One folder for each meeting |
| `$XDG_STATE_HOME/oat/` (default `~/.local/state/oat/`) | `echo.json` |
| `$XDG_CONFIG_HOME/oat/config.toml` (default `~/.config/oat/config.toml`) | Optional configuration file |

The meeting ID is the folder name: `YYYY-MM-DD-HHMM-<slug>`. The slug comes from the title, in ASCII, 40 characters at most. If the name exists, oat adds `-2`, `-3`, and so on.

| File | Content |
|---|---|
| `meta.json` | ID, title, start, end, language, model, echo mode, status |
| `transcript.jsonl` | One raw segment per line: `{"start":712.40,"end":716.90,"speaker":"them","text":"..."}` |
| `transcript.md` | The clean transcript, sorted by time, rewritten every 10 s and at stop |
| `notes.md` | The notepad exactly as you typed it, autosaved 2 s after the last key and at stop |
| `summary.md` | The notes that Claude writes through MCP |
| `.lock` | The PID of the process that records |
| `chunks/`, `failed/` | WAV files that wait for Groq, or that Groq refused |
| `audio/` | WAV files that `keep_audio` keeps after transcription |

The status of a meeting is `recording`, `processing`, `done`, or `interrupted`. At stop, the status changes to `processing` while chunks remain, then to `done`. If a meeting has the status `recording` or `processing` and the PID in `.lock` is not alive, oat changes the status to `interrupted`. The drain then sends the leftover chunks, and the status becomes `done`.

oat writes each JSONL line with one `write` call on a file opened with `O_APPEND`. A reader ignores a last line that has no newline. oat writes `meta.json`, `notes.md`, `transcript.md`, and `summary.md` to a temporary file first and then renames it.

`store.Clean` gives the transcript that the HUD, `transcript.md`, and the MCP server show. It sorts the segments by start time and applies the echo text filter. It joins consecutive segments of one speaker with a gap of less than 2 seconds. It is a pure function.

A line in `transcript.md` has this form:

```
[00:12:05] **Me:** Acho que dá, se a API ficar pronta até sexta.
```

## Configuration

The configuration file is optional. These are the keys and their defaults:

```toml
lang = "pt"                        # ISO 639-1 code, or "auto"
model = "whisper-large-v3-turbo"   # or "whisper-large-v3"
echo = "auto"                      # auto, on, off
keep_audio = false
```

The flags `--lang`, `--model`, `--echo`, and `--keep-audio` override the file.

oat looks for the Groq key in this order and uses the first value that it finds:

1. The environment variable `GROQ_API_KEY`.
2. `env.GROQ_API_KEY` in `~/.claude/settings.local.json`.
3. `env.GROQ_API_KEY` in `~/.claude/settings.json`.

If `CLAUDE_CONFIG_DIR` is set, oat reads the two files from that folder in place of `~/.claude`. If no key exists, oat stops before it records and names the three places. `oat doctor` shows the source of the key and the key in masked form, for example `gsk_…a1b2`.

## HUD

The HUD uses rounded borders, dim gray text, and three colors: the Claude orange `#D97757` for accents, green for "Me", and blue for "Them". Lip Gloss detects a light or a dark terminal background and picks matching shades. The HUD never waits on I/O. The session goroutines send events on a channel, and a `tea.Cmd` reads them.

### Recording screen

```
╭─ oat ──────────────────────────────────────────────── ● REC 00:12:34 ─╮
│ Weekly sync with Acme                         pt · turbo · echo on     │
╰────────────────────────────────────────────────────────────────────────╯
  Me    ▂▃▅▇▆▃▂▁  ━━━━━━━━━──────        Them  ▁▁▂▃▂▁▁  ━━━━──────────

  Transcript                                 │  Notes
  00:11:58  Them  Então, sobre o prazo da    │  - prazo: fim de outubro
                  entrega, a gente pensou…   │  - Acme quer demo semana
  00:12:05  Me    Acho que dá, se a API      │    que vem
                  ficar pronta até sexta.    │  - █
  00:12:20  Them  Perfeito.                  │
  ⠋ 2 chunks transcribing                    │
 ─────────────────────────────────────────────────────────────────────────
  tab focus · ctrl+p pause · ctrl+l lang · ctrl+s stop · ? help
```

The meters show the level of each stream, with a short history and a bar. The transcript pane takes 60% of the width and follows the newest line. The notes pane takes 40% and has the focus at start, so you can type at once. If the terminal is narrower than 90 columns, the HUD shows one pane at a time. The minimum size is 60 columns by 16 lines.

| Key | Action |
|---|---|
| `tab` | Move the focus between transcript and notes |
| `ctrl+p` | Pause or resume |
| `ctrl+l` | Change the language for the next chunks: `pt`, `en`, `auto` |
| `ctrl+s` | Stop and save |
| `ctrl+c` | Ask "Stop and save? y/n". oat never discards a recording |
| `?` | Show the help overlay. This key works in the transcript pane. In the notes pane, it types a question mark |
| Arrows, `pgup`, `pgdn`, `end` | Scroll the transcript. `end` follows the newest line again |

After the stop, the screen shows the progress of the queue. When the queue is empty, it shows the folder path and the hint `/mcp__oat__enhance`, then it goes back to the home screen. If you press `q` during the drain, oat quits, and the next start finishes the drain.

### Home screen

Each row shows the date, the time, the title, the length, the status, and a mark for notes and for a summary. The status marks are `●` recording, `⠋` processing, `✓` done, and `!` interrupted.

| Key | Action |
|---|---|
| `n` | New meeting. A text field asks for the title. An empty title gives "Meeting HH:MM" |
| `enter` | Open the viewer |
| `/` | Filter the list by title |
| `d` | Delete the meeting folder, after a `y/n` question |
| `q` | Quit |

### Viewer

The viewer shows a past meeting with the transcript on the left and the notes on the right. If `summary.md` exists, the key `s` shows it in the right pane. The key `e` opens the notes for editing. The key `esc` goes back to the home screen.

## MCP server

`oat mcp` runs the server on stdio with the official MCP Go SDK. It reads the files for each call and keeps no cache, so a live meeting always shows its newest lines. Only `save_summary` writes.

| Tool | Input | Output |
|---|---|---|
| `list_meetings` | `limit` (default 20), `query` (optional, matches the title) | ID, title, start, length, status, notes yes or no, summary yes or no |
| `get_meeting` | `id`, `since` (seconds, optional), `offset` and `limit` (lines, default 400) | Metadata, notes, summary, and the clean transcript in Markdown. If more lines exist, a `next_offset` line at the end |
| `search_meetings` | `query`, `limit` (default 20) | Matching lines from transcripts and notes: meeting ID, time, speaker, text |
| `save_summary` | `id`, `markdown` | Writes `summary.md` and returns its path |

The `since` field returns only the lines that start at or after that second of the meeting. The `id` field accepts a meeting ID, `latest`, or `live`. The value `live` means the meeting with the status `recording` and a live PID in `.lock`. If no meeting records, the tool returns the error "No recording in progress". The search ignores case and accents.

The `offset` and `limit` fields exist because Claude Code limits the output of an MCP tool to 25,000 tokens by default. A one-hour meeting is about 15,000 tokens.

The prompt `enhance` takes an optional `id` (default `latest`). It tells Claude to read the meeting with `get_meeting`, and to write notes in the language of the meeting. The notes have four parts: a summary, the decisions, the action items with their owners, and your notes expanded with facts from the transcript. Then Claude calls `save_summary`. In Claude Code, the prompt shows as `/mcp__oat__enhance`.

## Setup

The only runtime dependencies are `parec` and `pactl` from `pulseaudio-utils`. Pop!_OS has them by default. The build needs Go 1.25, which Go 1.24 downloads by itself because `GOTOOLCHAIN` is `auto`.

```
cd ~/oat && make install
oat doctor
```

`make install` builds the binary with `-trimpath -ldflags "-s -w"` to `~/.local/bin/oat` and then runs `oat setup`. `oat setup` runs `claude mcp get oat` first. If that command fails, `oat setup` runs `claude mcp add --scope user oat -- <absolute path> mcp`. If the `claude` command does not exist, it prints the command for you to run.

`oat doctor` tests these items and prints a fix for each failure:

1. `parec` and `pactl` exist.
2. The Pulse server answers, with a default source and a default sink.
3. A Groq key exists. The output shows its source and its masked value.
4. The Groq key works. oat calls `GET /openai/v1/models`, which uses no audio quota.
5. The MCP server is registered in Claude Code.
6. The data folder is writable.
7. No echo-cancel module from a crash remains.

## Error handling

| Condition | Behavior |
|---|---|
| No Groq key | oat stops before it records and names the three places for the key |
| A recording runs in another terminal | `oat new` stops with the message "A recording is in progress in another terminal" |
| Groq rejects the key | The recording continues. Chunks stay on disk, and the HUD shows a red banner. The next start drains them after you fix the key |
| Network down | Retry with backoff. The HUD shows the size of the queue |
| `parec` stops | Start again, up to 3 times in 10 s. Then a warning, and the meeting continues with the other stream |
| Echo-cancel module fails | Record without it. The text filter still runs. The HUD shows a yellow warning |
| Disk write fails | The HUD shows a red banner, and oat stops the recording, so that no audio is lost without notice |
| `SIGINT`, `SIGTERM`, `SIGHUP` | The same as `ctrl+s`. oat flushes the chunks to disk and removes the echo-cancel module |
| Crash or `SIGKILL` | The next start marks the meeting `interrupted`, drains the chunks, and removes the echo-cancel module |

## Testing

The unit tests make no network calls and need no audio device.

| Package | Test |
|---|---|
| `chunk` | Synthetic PCM of tones and silence gives the expected cuts, drops, and pre-roll |
| `config` | A temporary HOME tests the key lookup order, `CLAUDE_CONFIG_DIR`, and flag overrides |
| `groq` | An `httptest` server returns 200, 429 with `retry-after`, 500, and 401 |
| `transcribe` | A fake Groq client tests retry, the segment filters, and the time offsets |
| `store` | `Clean` tests sort, echo filter, and joins. Tests also cover the ID slug, a partial JSONL line, and status changes |
| `audio` | Tests parse recorded `pactl` output for headphones, Bluetooth, speakers, and HDMI |
| `mcpserver` | The SDK in-memory transport calls each tool against sample meeting folders |

Manual acceptance test:

1. Run `oat doctor`. All items pass.
2. Use headphones. Play a Portuguese video, talk over it for 2 minutes, and stop. The transcript labels both voices correctly and has no duplicate lines.
3. Do step 2 again on the laptop speakers with `echo = auto`. The echo-cancel module loads. After the stop, the default sink is the original one, and `pactl list modules short` shows no module from oat.
4. Turn off the network during a recording. The queue grows. Turn the network on. The queue drains.
5. Run `kill -9` on oat during a recording. The next start shows the meeting as interrupted, drains the chunks, and removes the module.
6. In Claude Code, ask for the list of meetings, ask a question about the last meeting, and run `/mcp__oat__enhance`. Claude writes `summary.md`.

## Risks

The arguments of `module-echo-cancel` in the Pulse layer of PipeWire 1.0.3 are not tested on this machine. The implementation plan starts with a short test of the module. If it fails, the text filter is the only layer against echo.

The VAD constants come from common values, not from measurements. They need tuning with real meetings.

The Groq free tier can limit a long meeting on two streams. The HUD shows each wait after a 429, so the delay is visible.
