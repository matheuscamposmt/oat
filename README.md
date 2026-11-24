# oat

<p align="center"><b>Meeting notes in your terminal, transcribed and summarized with the Claude Code you already use.</b></p>

<p align="center">
  <img src="docs/hud.png" alt="The oat recording screen: a live transcript on the left and your notes on the right" width="920">
</p>

oat records any meeting on Linux: Google Meet, Zoom, Teams, or a call in the browser. It captures your microphone and the system audio as two separate streams, so the transcript knows who said what. The first words of a phrase show up in the HUD about 2 seconds after the person starts to speak, the rest follows as it is said, and the words get corrected as the speech goes on. You type rough notes next to them. When you stop, Claude Code merges your notes with the transcript into a summary, and oat saves it next to the meeting.

oat needs no new account and no API key. The transcription uses the speech-to-text service of the Claude Code voice mode, with the login that Claude Code already has. The summary runs in Claude Code too. If Claude Code works on your machine, oat works too.

- No keys to manage: `oat doctor` finds your Claude Code login and tests it.
- A live transcript, with the corrections of the speech model as they happen.
- A summary after each meeting: summary, decisions, action items, and your notes expanded. Press `ctrl+b` during the meeting to mark a moment, and the summary explains it.
- Two audio streams that never mix, "Me" and "Them", plus echo cancel for speakers.
- Plain files: each meeting is a folder of Markdown and JSONL on your disk.
- Each audio chunk goes to disk before it goes to the network, so a crash loses at most 30 seconds.
- Claude Code can answer questions about past meetings, such as "what did they decide about the budget last week?"

## Quick start

Download the static binary for Linux on x86-64:

```sh
curl -fsSL -o ~/.local/bin/oat https://github.com/matheuscamposmt/oat/releases/latest/download/oat-linux-amd64
chmod +x ~/.local/bin/oat
```

Then install the Claude Code plugin. In Claude Code, run:

```
/plugin marketplace add matheuscamposmt/oat
/plugin install oat@oat
```

The plugin gives Claude Code the oat MCP server and the `/oat:transcribe` skill. It does not carry the binary, so `oat` must be on your PATH. Then test the setup and record:

```sh
oat doctor        # tests the audio, the Claude login, and the MCP server
oat new "Weekly sync"
```

Without the plugin, run `oat setup` instead. It registers the MCP server in Claude Code at user scope. Use one of the two, not both: with both, Claude Code runs the server twice. `oat doctor` warns about it, and `claude mcp remove --scope user oat` fixes it.

Or build it from source with `make install` (see [Install](#install)).

Press `ctrl+s` to stop. oat writes the summary a few seconds later, and `enter` opens it.

This is an early version. It runs on Linux only and was tested on Pop!_OS 22.04 with PipeWire.

## How it works

`oat new "Title"` starts a recording. Two `parec` processes capture the mic ("Me") and the monitor of your default output ("Them"). The two streams never mix, so the transcript knows who said what.

Each stream is cut at pauses into chunks of 10 to 30 seconds. A chunk opens its own connection to the Claude Code voice stream after half a second of speech, and its audio goes out in real time. The first partial text of a chunk comes back about 2 seconds after the speech starts (1.75 s median in tests), and then it follows the speech. The HUD shows it dim, with an ellipsis. At the pause, oat closes the connection, gets the final text, and writes it to the transcript. Short noises, such as a key press, open no connection.

A connection lives for one chunk only. In tests, one connection for a whole meeting returned no final text until it closed, so a failure would lose everything. The server also refuses a sixth connection at the same time, and oat keeps at most four open.

If a live connection fails, the chunk file on disk goes to a queue, and oat sends it again as fast as the connection takes it. With `live = false`, every chunk takes that path, and a line shows up 10 to 30 seconds after it was said.

The voice stream gives text without times. oat splits the text of a chunk into sentences and gives each sentence a part of the chunk in proportion to its length. The times in the transcript are therefore estimates inside each chunk.

While the meeting runs you type notes in the right pane. Notes are saved 2 seconds after you stop typing, and each audio chunk is written to disk as soon as it is cut, so a crash loses the audio still in memory (under 30 seconds) and nothing else.

After the stop, oat runs `claude -p` in safe mode, with no tools, and gives it your notes and the transcript. It saves the answer as `summary.md`, in the language of the meeting. If the meeting still has the default title, such as "Meeting 14:30", the summary gives it a better one. The summary runs in its own process, so it ends even if you quit oat, and a desktop notification says when it is ready. `oat summary [id]` writes it again from the command line.

`oat mcp` gives an MCP client access to the meetings. In Claude Code, `/mcp__oat__enhance` writes the summary inside your Claude Code session, where you can ask for changes.

Audio chunks go to the voice endpoint at api.anthropic.com (or to Groq, with `transcriber = "groq"`) and are deleted once they are transcribed. Anthropic does not document which service transcribes the audio behind that endpoint. Set `keep_audio = true` to keep the chunks. Transcripts, notes, and summaries stay on your disk.

## Requirements

- Linux with PipeWire (and its Pulse layer) or PulseAudio
- `parec` and `pactl`, from the `pulseaudio-utils` package
- `ffmpeg`, for `oat transcribe` only
- Go 1.25 or later to build. Go 1.24 downloads 1.25 on its own.
- Claude Code, logged in with a claude.ai account. With `transcriber = "groq"`, a Groq API key instead.

## Install

With the default backend, oat reads the claude.ai token from `~/.claude/.credentials.json` (or `$CLAUDE_CONFIG_DIR/.credentials.json`). Run `claude` once and log in. oat never writes that file and never renews the token. Claude Code renews it when it runs.

To use Groq, set `transcriber = "groq"` and give oat your Groq key in one of two ways. Export it in your shell:

```sh
export GROQ_API_KEY=gsk_...
```

Or put it in `~/.config/oat/config.toml` and keep the file readable by you only:

```toml
groq_api_key = "gsk_..."
```

```sh
chmod 600 ~/.config/oat/config.toml
```

Then build and install:

```sh
git clone https://github.com/matheuscamposmt/oat
cd oat
make install
oat doctor
```

`make install` builds a static binary into `~/.local/bin/oat` and runs `oat setup`, which registers the MCP server in Claude Code at user scope. When the oat plugin is installed, `oat setup` does nothing. `oat doctor` checks the audio stack, the Claude login and the voice stream (or the Groq key), the MCP server, `ffmpeg`, and the data folder. It prints a fix under anything that fails.

`go install github.com/matheuscamposmt/oat/cmd/oat@latest` also works. Run `oat setup` yourself afterwards. With that install, `oat version` prints `dev`.

## Record a meeting

```sh
oat new "Weekly sync"             # Portuguese by default
oat new "Weekly sync" --lang en
oat                               # the list of past meetings; press n to start one
```

The notes pane has the focus when the recording starts, so you can type right away.

| Key | Action |
|---|---|
| `tab` | Move the focus between the transcript and the notes |
| `ctrl+b` | Mark this moment in the notes; the summary explains it |
| `ctrl+p` | Pause or resume |
| `ctrl+l` | Change the language for the next chunks: `pt`, `en`, `auto` |
| `ctrl+s` | Stop and save |
| `ctrl+c` | Stop and save, after a yes/no question |
| `?` | Show the keys (in the transcript pane) |

After you stop, oat finishes the last chunks and writes the summary. `enter` opens the meeting on its summary, and any other key goes back to the list. If you quit before the chunks are done, the next start of oat finishes them.

In the list, `s` writes the summary of the selected meeting again. In a meeting, `s` switches between the summary and your notes, `y` copies the one on screen, and `r` writes the summary again.

## Transcribe an audio file

```sh
oat transcribe "WhatsApp Ptt 2026-11-24 at 11.57.06.ogg"
oat transcribe --keyterms "Entra ID,GST" --lang auto recording.m4a
```

`oat transcribe` prints the text of an audio file, one paragraph per chunk. It needs `ffmpeg` (`sudo apt install ffmpeg`), which decodes any format it knows. oat cuts the audio at pauses, as in a meeting, and sends up to four chunks at the same time with Claude, two with Groq. Errors and retries go to stderr, and stdout gets the text only when every chunk worked.

`--keyterms` lists names and acronyms, separated by commas, so that the backend spells them right. With the Claude backend, use ASCII only: Claude Code removes accented letters from the terms, so "Vinícius" becomes "Vincius". The list stops at 1024 characters. With `--transcriber groq`, the terms go to the Whisper prompt, and accents are fine.

The flags `--transcriber`, `--lang`, and `--model` work as in `oat new`. The file does not become a meeting.

In Claude Code, the plugin skill `/oat:transcribe` runs this command. It finds the file, builds the key terms from the conversation, and shows the text.

## Ask Claude about your meetings

In Claude Code, ask something like "what did they say about the budget in the last meeting?". Claude has these tools:

| Tool | Purpose |
|---|---|
| `list_meetings` | Lists the meetings, newest first |
| `get_meeting` | Reads the notes, the summary, and the transcript. The ID `live` reads the recording in progress |
| `search_meetings` | Finds text in all transcripts and notes |
| `save_summary` | Saves meeting notes as `summary.md` |

To write the summary inside your Claude Code session, run `/mcp__oat__enhance`. Any other MCP client that can start a stdio server can use `oat mcp` the same way.

## Echo

With speakers, the microphone also hears the other people, so their words show up twice. oat handles this in two layers.

The first is the PipeWire echo-cancel module. In the default `auto` mode, oat loads it for every output except headphones. While oat records, your default output is `oat_ec_sink`. oat restores the old output when you stop. If oat crashes, the next start (or `oat doctor`) removes the module.

The second is a text filter that drops a "Me" line when it repeats a "Them" line from the same seconds.

## Configuration

`~/.config/oat/config.toml` is optional. These are the keys and their defaults:

```toml
transcriber = "claude"             # claude or groq
live = true                        # claude only: send the audio while people speak
auto_summary = true                # write summary.md with claude -p after each meeting
lang = "pt"                        # ISO 639-1 code, or "auto"
model = "whisper-large-v3-turbo"   # Groq only: or "whisper-large-v3"
echo = "auto"                      # auto, on, off
keep_audio = false                 # keep the WAV chunks in audio/
groq_api_key = ""                  # Groq only: used when GROQ_API_KEY is not set
```

The flags `--transcriber`, `--no-live`, `--lang`, `--model`, `--echo`, and `--keep-audio` override the file.

## Files

Each meeting is a folder in `~/.local/share/oat/meetings/`:

| File | Content |
|---|---|
| `meta.json` | Title, start, end, language, model, status |
| `transcript.jsonl` | One raw segment per line |
| `transcript.md` | The readable transcript |
| `notes.md` | Your notes |
| `summary.md` | The notes Claude wrote |
| `chunks/` | Audio waiting for the transcription service |
| `failed/` | Audio that the transcription service refused |
| `audio/` | Audio kept with `keep_audio = true` |
| `.lock` | The PID of the process that is recording |

## The Claude voice stream

oat uses `wss://api.anthropic.com/api/ws/speech_to_text/voice_stream`, the endpoint of the Claude Code voice mode. Anthropic does not document it, so keep these limits in mind:

- The endpoint can change or close in any Claude Code release. If `oat doctor` shows `✗ Claude voice`, set `transcriber = "groq"`.
- oat uses the claude.ai login of Claude Code outside Claude Code. Read the terms of your plan before you use it.
- The endpoint can drop the last words of a chunk that ends in the middle of a sentence.
- If the token expires during a meeting, the chunks wait on disk and oat tries again. Open Claude Code to renew the token.
- The server allows about five connections at the same time for one login. oat keeps at most four. If the voice mode of Claude Code runs at the same time, oat can get a rate limit, and it waits a few seconds.

## Groq limits

The Groq free tier caps the audio seconds per hour. Two streams can send up to twice the length of the meeting, which is why oat skips chunks with less than a second of speech. When Groq rate-limits a request, the HUD shows how long it waits.

## License

MIT. See `LICENSE`.
