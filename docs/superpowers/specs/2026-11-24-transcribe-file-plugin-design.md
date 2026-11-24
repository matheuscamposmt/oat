# Transcribe an audio file, and oat as a Claude Code plugin

## Intent

The user gets voice messages and recordings (WhatsApp `.ogg`, `.m4a`, `.mp3`) and wants their text inside Claude Code with one command. The text must come from the same Claude Code voice stream that oat uses for meetings, so no API key is needed. Names and acronyms of the conversation, such as "Entra ID" and "GST", must come out right.

Today the user has a personal `transcribe-audio` skill that sends the file to Groq, and oat has no command for a file.

## Test results (2026-11-24)

The test client was a copy of `internal/claudestt` with one change: an `x-config-keyterms` header. Claude Code sends that header in its voice mode. The audio was an 87 s WhatsApp message in Portuguese, sent in one stream, two runs for each case.

| Case | "login do ..." | Runs |
|---|---|---|
| No context | "EntrID" | 2 of 2 |
| `x-config-keyterms: Entra ID,GST,...` | "Entra ID" | 2 of 2 |
| Spoken prefix "Contexto: login pelo Entra ID..." and 1 s of silence | "Entra ID" | 2 of 2 |

- The header changes only the listed terms. The rest of the text stayed the same, word for word.
- Claude Code removes every non-ASCII character from a term before it sends the header. "Vinicius" in the list turned "Vinícius" into "Vinicius" in the text.
- The spoken prefix also shows up in the transcript and needs a text-to-speech tool.
- The server sends only `TranscriptText` and `TranscriptEndpoint`. No message names the model.
- From the live-transcript tests: 13 min of audio sent at once in one stream lost more than half of the text.

## Approaches

| Approach | Trade-off | Decision |
|---|---|---|
| `oat transcribe` in oat, and a plugin with the skill and the MCP server | One client for the voice stream. Long files use the chunker. One install for the skill and the MCP server. | Chosen |
| A separate binary with a copy of `claudestt`, and a personal skill | Faster to make. Two copies of an undocumented client, and long files lose text. | Rejected |
| A spoken prefix for context | Works, but the prefix comes back in the text and needs text-to-speech. | Rejected |
| A personal skill in `~/.claude/skills` instead of a plugin | Works on one machine only, with no version, and `oat setup` stays a separate step. | Rejected |

## Design

### `oat transcribe`

```
oat transcribe [--transcriber S] [--lang CODE] [--model NAME] [--keyterms "A,B"] FILE
```

- `ffmpeg` decodes FILE to 16 kHz mono s16le on stdout. oat reads it in 20 ms frames and gives them to `chunk.Chunker`, which cuts at pauses into chunks of 10 to 30 s. `Flush` ends the last chunk.
- Each chunk goes to `Client.Transcribe`, the same call as the replay path of a meeting. Up to `claudestt.MaxStreams` chunks run at the same time with the Claude backend, and 2 with Groq.
- A `RateLimitError` or a `TemporaryError` waits and tries the chunk again, like the meeting pipeline. An `AuthError` or a `RequestError` stops the command with the message of the error.
- The text of the chunks goes to stdout in time order, one paragraph per chunk. Errors and progress go to stderr. Exit code 0 only when every chunk has text or was silence.
- `--lang` defaults to the `lang` of the configuration. `--transcriber` and `--model` work as in `oat new`.
- Missing `ffmpeg` stops the command with the install hint. `oat doctor` reports `ffmpeg` as optional.

### Key terms

- `stt.Options` gets `Keyterms []string`.
- `claudestt` sends them in `x-config-keyterms` with the rules of Claude Code: commas become spaces, non-ASCII characters are removed, duplicates and empty terms are dropped, and terms join with commas up to the length limit of Claude Code. The plan finds that limit in the Claude Code source.
- `groq` sends them in the `prompt` field as plain text, with accents, because Whisper takes free text.
- Meetings do not use key terms in this change.

### The plugin

The plugin lives in `plugin/`, not at the repository root, so that a `.mcp.json` at the root does not become a project server for people who work on the oat code.

- `plugin/.claude-plugin/plugin.json`: name `oat`, version, description.
- `plugin/.mcp.json`: the `oat` server, command `oat`, args `["mcp"]`. It expects `oat` on the PATH.
- `plugin/skills/transcribe/SKILL.md`: the `/oat:transcribe` skill. It:
  1. Takes the file path from the arguments or from the conversation.
  2. Builds the key terms from the conversation and the project: proper names, acronyms, product names. It keeps names with accents out of the list, because the Claude backend removes the accents.
  3. Runs `oat transcribe --keyterms "..." FILE` and shows the text.
  4. Offers to save the text as a `.txt` file next to the audio.
- `.claude-plugin/marketplace.json` at the root has `name: "oat"`, `owner.name`, and one plugin entry with `name: "oat"` and `source: "./plugin"`. A relative source resolves from the repository root. The user installs it with `/plugin marketplace add matheuscamposmt/oat` and `/plugin install oat@oat`. Before the merge, `/plugin marketplace add matheuscamposmt/oat#transcribe-file-plugin` installs the branch.
- The plugin does not carry the binary. The plugin system has no build step, so `oat` comes from the release download or `make install`, as today.

Facts from the Claude Code documentation (checked on 2026-11-24):

- The tools of a plugin server are named `mcp__plugin_oat_oat__<tool>`, and `claude mcp list` shows the server as `plugin:oat:oat`. `claude mcp get plugin:oat:oat` works. It was tested with the Playwright plugin.
- Claude Code skips a plugin server when a user-scope server has the same command. The documentation does not say how it compares commands. Today the user-scope `oat` runs an absolute path, and the plugin runs `oat` from the PATH, so both can run, with duplicate tools.
- The documentation does not say how the prompts of a plugin server are named. `/mcp__oat__enhance` can get a new name. The implementation reads the real name in a session and updates the README.

Changes that follow from these facts:

- `oat doctor` accepts `oat` or `plugin:oat:oat`. When both exist, it shows a warning with the fix `claude mcp remove --scope user oat`.
- `oat setup` does nothing when `plugin:oat:oat` exists. `make install` runs `oat setup`, so it does not create the duplicate again.
- On this machine, after the plugin works: `claude mcp remove --scope user oat`.
- The README shows the plugin as the main install path and `oat setup` as the path without the plugin.
- Local test: `claude plugin validate ./plugin`, `claude plugin validate .`, and `claude --plugin-dir ./plugin`.

## Acceptance

The tests above used one stream. `oat transcribe` sends chunks, and the chunker drops a chunk with less than 1 s of speech. So the change is done when:

1. The 87 s WhatsApp message through `oat transcribe --keyterms "Entra ID,GST"` gives "Entra ID" in both places, and no sentence of the one-stream text is missing.
2. An 8 s clip of the same message gives its full text.
3. A file with no speech gives no text and exit code 0.
4. `/oat:transcribe` in a session started with `claude --plugin-dir ./plugin` builds the key terms from the conversation and shows the text.

## Goals and non-goals

- Goal: one command that turns a local audio file into text, with the right names, and no API key.
- Goal: the plugin installs the MCP server and the skill together.
- Non-goal: the file does not become a meeting. It does not show in `oat` or in the MCP tools.
- Non-goal: no times or speakers in the output of `oat transcribe`.
- Non-goal: no change to the meeting path.

## Left to the user

- After the plugin works, remove the personal `transcribe-audio` skill, so that two skills do not answer the same request. Proposed: yes.

## Calls made by the author

- The skill is `transcribe` in English, as the rest of oat, so the command is `/oat:transcribe`.
- No automatic switch to Groq when the Claude backend fails. The user picks the backend with `--transcriber`.
- Text only on stdout. Saving a file is the job of the skill.

## Addendum: key terms in meetings and named lists (2026-11-24)

The meeting of 2026-11-24 came out with "raiz" and "RAID" for RAIS, "webcontas" for LabContas, "Enter ID" for Entra ID, "The Beaver" for DBeaver, and "Flow 3" for Airflow 3. Meetings send no key terms today. The user records meetings about different projects (CVM, NEXO), so one list would push the terms of one project into the others.

Claude Code builds its own voice key terms with no configuration: a fixed base of 14 coding terms, the project folder name, and the words of the git branch, at most 50 terms. oat follows the same idea.

### Sources of the terms, in priority order until the limit

1. `--keyterms` of `oat new` or `oat transcribe`.
2. The named list chosen for the recording.
3. The words of the meeting title, split like Claude Code splits a branch (spaces, `-_./`, camelCase), 3 to 20 characters each.
4. A generic base in the code: MCP, OAuth, webhook, worktree, subagent, JSON, TypeScript, localhost, regex, codebase, gRPC.

The limit is 50 terms and 1024 characters, the same as Claude Code.

### Named lists

```toml
[keyterms.cvm-app]
folder = "/home/user/projects/cvm-app"
terms = ["RAIS", "LabContas", "Entra ID"]
```

- `oat new --keyterms-set NAME` chooses a list. Without it, oat chooses the first list whose name appears in the meeting title, case and accents ignored. With no match, no list.
- `oat transcribe` takes the same `--keyterms-set`.

### `oat keyterms`

- `oat keyterms list [NAME]`: the lists, or the effective terms of one list with the origin of each term. It warns about accented terms (Claude drops the accents) and about the limit.
- `oat keyterms add NAME TERM...` and `oat keyterms rm NAME TERM...`.
- `oat keyterms scan FOLDER [--name NAME]`: runs `claude -p` with only `Read`, `Glob` and `Grep`, with access to FOLDER only, and asks for proper names, acronyms, systems and products from the files that describe the project (README, CLAUDE.md, AGENTS.md, docs, glossaries, folder names), at most 50 terms, no accents. oat shows the proposed list, the user removes terms and confirms, and only then oat saves it. The list name defaults to the folder name, and the folder path is saved with the list.
- `oat keyterms scan --all`: scans again every list that has a folder.
- `oat setup` offers the scan: it asks for folders, one per line, and runs the scan for each one. Before the first scan it says that the documents of the folder go to Claude, the same as in a Claude Code session there.

### Meetings

- The live stream and the replay queue send the key terms of the recording. The terms are fixed when the recording starts.
- The terms used are saved in the meeting metadata, so a summary written again later knows them.

### Plugin

- A skill `/oat:keyterms` runs the same commands: scan the current project, or add a term after a wrong transcription ("RAIS came out as raiz").

### Calls made by the author

- No automatic update of the lists from the summaries in this change.
- The scan reads descriptive files and names, not the whole code, to keep it short and cheap.
