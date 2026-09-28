# oat

oat records meetings on Linux from the terminal. It captures your microphone and the system audio as two separate streams. The Groq Whisper API transcribes both streams, and oat saves the transcript next to the notes that you type during the meeting. Claude Code reads the meetings through the MCP server that is part of oat.

## Requirements

You need these items:

1. Linux with PipeWire and its Pulse layer, or PulseAudio.
2. The commands `parec` and `pactl` from `pulseaudio-utils`.
3. Go 1.25 or later to build. Go 1.24 downloads Go 1.25 by itself.
4. A Groq API key.

## Install

1. Put your Groq key in the `env` block of `~/.claude/settings.local.json`:

   ```json
   {
     "env": {
       "GROQ_API_KEY": "gsk_..."
     }
   }
   ```

2. Build and install the binary. This command also registers the MCP server in Claude Code:

   ```
   make install
   ```

3. Test the environment:

   ```
   oat doctor
   ```

If a test fails, `oat doctor` prints the fix under it.

## Record a meeting

Start a recording at once:

```
oat new "Weekly sync"
oat new "Weekly sync" --lang en
```

Or run `oat` to open the list of meetings, and press `n`.

During the recording, the notes pane has the focus, so you can type at once.

| Key | Action |
|---|---|
| `tab` | Move the focus between the transcript and the notes |
| `ctrl+p` | Pause or resume |
| `ctrl+l` | Change the language for the next chunks: `pt`, `en`, `auto` |
| `ctrl+s` | Stop and save |
| `ctrl+c` | Stop and save, after a question |
| `?` | Show the keys. This key works in the transcript pane |

After the stop, oat sends the last chunks to Groq. If you quit before that ends, the next start of oat sends them.

## Use the meetings in Claude Code

Ask Claude about your meetings, for example "what did they say about the budget in the last meeting?". Claude uses these MCP tools:

| Tool | Purpose |
|---|---|
| `list_meetings` | Lists the meetings, newest first |
| `get_meeting` | Reads the notes, the summary, and the transcript. The ID `live` reads the recording in progress |
| `search_meetings` | Finds text in all transcripts and notes |
| `save_summary` | Saves meeting notes as `summary.md` |

To get Granola-style notes, run `/mcp__oat__enhance` in Claude Code. Claude merges your notes with the transcript and saves the result in `summary.md`.

## Echo

On speakers, the microphone also records the other people. oat uses two layers against this echo.

The first layer is the PipeWire echo-cancel module. In the default `auto` mode, oat loads the module for all outputs except headphones. While oat records, your default output is `oat_ec_sink`, and oat restores the old output at the stop. If oat crashes, the next start of oat, or `oat doctor`, removes the module.

The second layer is a text filter. It drops a "Me" line that repeats a "Them" line from the same seconds.

## Configuration

The file `~/.config/oat/config.toml` is optional. These are the keys and their defaults:

```toml
lang = "pt"                        # ISO 639-1 code, or "auto"
model = "whisper-large-v3-turbo"   # or "whisper-large-v3"
echo = "auto"                      # auto, on, off
keep_audio = false                 # keep the WAV chunks in audio/
```

The flags `--lang`, `--model`, `--echo`, and `--keep-audio` override the file.

oat reads the Groq key from the first place that has it:

1. The environment variable `GROQ_API_KEY`.
2. The `env` block of `~/.claude/settings.local.json`.
3. The `env` block of `~/.claude/settings.json`.

## Files

Each meeting has a folder in `~/.local/share/oat/meetings/`:

| File | Content |
|---|---|
| `meta.json` | Title, start, end, language, model, status |
| `transcript.jsonl` | One raw segment per line |
| `transcript.md` | The readable transcript |
| `notes.md` | Your notes |
| `summary.md` | The notes that Claude wrote |
| `chunks/` | Audio that waits for Groq |

## Groq limits

The Groq free tier limits the audio seconds per hour. Two streams can send up to twice the length of the meeting. oat does not send chunks with less than 1 second of speech. After a rate limit, the HUD shows the wait.
