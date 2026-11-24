# Transcribe a file and the oat plugin: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `oat transcribe FILE` prints the text of an audio file with key terms, and a Claude Code plugin ships the oat MCP server and the `/oat:transcribe` skill.

**Architecture:** `ffmpeg` decodes the file to 16 kHz mono s16le. The existing `chunk.Chunker` cuts it at pauses, and each chunk goes to the existing `transcribe.Client` with up to N streams at once. Key terms travel in `stt.Options` to the Claude header or the Groq prompt. The plugin lives in `plugin/` and a root marketplace points at it.

**Tech Stack:** Go 1.25, `github.com/coder/websocket`, `ffmpeg`, Claude Code plugins.

**Spec:** `docs/superpowers/specs/2026-11-24-transcribe-file-plugin-design.md`

## Global Constraints

- Branch `transcribe-file-plugin` in `~/oat`. Commits in English, imperative, like the existing history.
- Comments and README in English, short sentences, as in the rest of oat.
- `x-config-keyterms`: commas become spaces, non-ASCII characters are removed, runs of spaces collapse, empty and duplicate terms are dropped, terms join with `,`, total length at most 1024 characters (Claude Code `N=1024`). A term that does not fit ends the list.
- No automatic switch between backends.
- The meeting path does not change. `Open` for live streams sends no key terms.
- `make test` (`go test -race ./...`) passes after each task.

## Review Focus

1. A path with spaces and accents ("WhatsApp Ptt 2026-11-24 at 11.57.06.ogg") must work: `ffmpeg` gets the path as one argument, never through a shell. Test in Task 3.
2. A file that is not audio must stop with the `ffmpeg` error text on stderr and exit 1, with nothing on stdout. Test in Task 3.
3. A long file must not sit whole in memory: chunks stream from `ffmpeg` while earlier chunks transcribe. Test in Task 2 (the reader is consumed while the first chunk is still in flight).
4. A chunk that keeps failing must not hang the command forever: at most 6 tries, then exit 1 with the chunk start time. Test in Task 2.
5. Ctrl-C must stop `ffmpeg` and the streams: the context cancels both. Test in Task 2 (cancel returns `context.Canceled` and no goroutine leaks under `-race`).

---

### Task 1: Key terms in the backends

**Files:**
- Modify: `internal/stt/stt.go` (add `Keyterms []string` to `Options`)
- Create: `internal/claudestt/keyterms.go`
- Modify: `internal/claudestt/claudestt.go` (`dial` sets the header), `internal/claudestt/stream.go` (`open` takes the terms)
- Modify: `internal/groq/groq.go` (prompt)
- Test: `internal/claudestt/claudestt_test.go`, `internal/groq/groq_test.go`

**Interfaces:**
- Produces: `stt.Options.Keyterms []string`; `claudestt.EncodeKeyterms(terms []string) string`.
- `Transcribe` sends `opts.Keyterms`. `Open(ctx, lang, onText)` keeps its signature and sends none.

- [ ] **Step 1: Write the failing tests**

```go
func TestEncodeKeyterms(t *testing.T) {
	got := EncodeKeyterms([]string{"Entra ID", "GST", "Vinícius", "a,b", " ", "GST", "  Data   Mart "})
	if got != "Entra ID,GST,Vincius,a b,Data Mart" { t.Fatalf("got %q", got) }
	long := []string{strings.Repeat("x", 1000), strings.Repeat("y", 30), "z"}
	if got := EncodeKeyterms(long); got != strings.Repeat("x", 1000) { t.Fatalf("limit: %d chars", len(got)) }
}

func TestTranscribeSendsKeytermsHeader(t *testing.T) // fake server records r.Header.Get("x-config-keyterms") == "Entra ID,GST"
func TestTranscribeWithoutKeytermsSendsNoHeader(t *testing.T) // header absent (not empty string)
```

The limit case follows Claude Code: the loop stops at the first term that does not fit ("y..." needs 1031 characters), so "z" is not added.

In `groq_test.go`: `TestTranscribeSendsKeytermsInPrompt` with `Options{Prompt: "fim da frase", Keyterms: []string{"Entra ID", "Vinícius"}}` expects the field `prompt` == `"Entra ID, Vinícius. fim da frase"`; with an empty `Prompt` it expects `"Entra ID, Vinícius."`.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/claudestt/ ./internal/groq/ -run 'Keyterms' -v`
Expected: FAIL, `EncodeKeyterms` and `Keyterms` are undefined.

- [ ] **Step 3: Implement**

- `EncodeKeyterms` in `keyterms.go`, rules from Global Constraints.
- `open(outer, ctx, lang, keyterms string, onText)`, and `dial(ctx, outer, lang, keyterms string)` sets `x-config-keyterms` only when `keyterms != ""`. `Transcribe` passes `EncodeKeyterms(opts.Keyterms)`. `Open` and `Ping` pass `""`.
- Groq: when `opts.Keyterms` is not empty, `prompt = strings.Join(terms, ", ") + "."`, followed by `" " + opts.Prompt` when `Prompt` is not empty. Accents stay.

- [ ] **Step 4: Run the package tests**

Run: `go test -race ./internal/claudestt/ ./internal/groq/ ./internal/session/ ./internal/transcribe/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/stt internal/claudestt internal/groq
git commit -m "Send key terms to the Claude header and the Groq prompt"
```

### Task 2: Transcribe a PCM stream in chunks

**Files:**
- Create: `internal/filetx/filetx.go`
- Test: `internal/filetx/filetx_test.go`

**Interfaces:**
- Consumes: `chunk.New`, `(*Chunker).Push`, `(*Chunker).Flush`, `chunk.EncodeWAV`, `transcribe.Client`, `transcribe.Hallucination`, the `stt` error types.
- Produces:

```go
type Options struct {
	STT     stt.Options
	Workers int                                            // streams at the same time; 1 when zero
	Sleep   func(ctx context.Context, d time.Duration) error // nil: real sleep
	Log     func(format string, args ...any)                 // progress and retries; nil: none
}
type Paragraph struct {
	Start float64 // seconds from the start of the file
	Text  string
}
// Transcribe reads 16 kHz mono s16le PCM from r until EOF and returns the text of each chunk, in time order.
func Transcribe(ctx context.Context, r io.Reader, c transcribe.Client, o Options) ([]Paragraph, error)
```

- [ ] **Step 1: Write the failing tests**

A fake client returns a text per call from a map keyed by `len(wav)`, records calls, and can return a scripted error sequence. Build PCM with speech (a 440 Hz sine at -20 dBFS) and silence (zeros) so the chunker cuts at pauses.

```go
func TestTranscribeKeepsTimeOrder(t *testing.T)       // 3 speech blocks of 12 s split by 2 s of silence, Workers: 4, the fake answers the first chunk last; texts come back as [first, second, third] with ascending Start
func TestTranscribeSilenceGivesNoParagraphs(t *testing.T) // 20 s of zeros: no call, empty slice, nil error
func TestTranscribeDropsEmptyAndHallucinatedText(t *testing.T) // a chunk answers "" and one answers "Obrigado por assistir": neither is returned
func TestTranscribeRetriesRateLimitAndTemporary(t *testing.T)  // errors RateLimitError{RetryAfter: 5s}, TemporaryError, then text: one paragraph; Sleep got [5s, 2s]
func TestTranscribeGivesUpAfterSixTries(t *testing.T)          // TemporaryError forever: error mentions "after 6 tries" and the chunk start "0:00"
func TestTranscribeStopsOnAuthError(t *testing.T)              // AuthError: returned as is, no retry
func TestTranscribeStopsOnRequestError(t *testing.T)           // RequestError: returned as is, no retry
func TestTranscribePassesOptions(t *testing.T)                 // the fake sees Keyterms ["Entra ID"] and Language "pt"
func TestTranscribeStreamsWhileReading(t *testing.T)           // io.Pipe: the first chunk reaches the client before the writer writes the last block
func TestTranscribeCancel(t *testing.T)                        // cancel while the fake blocks: returns context.Canceled
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/filetx/ -v`
Expected: FAIL, package `filetx` has no `Transcribe`.

- [ ] **Step 3: Implement `Transcribe`**

- Read 640-byte frames (320 samples, 20 ms) with `io.ReadFull`. A short last frame is padded with zeros. `Push(idx, samples)` with a running frame index. After EOF, `Flush`.
- Each chunk goes to a worker pool of size `Workers` through a channel, with its index. Results land in a slice by index, so the order is the time order whatever the finish order is.
- Retry per chunk, at most 6 calls: `RateLimitError` sleeps `RetryAfter`; any other error that is not `AuthError`, `RequestError`, or a context error sleeps 2, 4, 8, 16, 32 s. After the sixth failure: `fmt.Errorf("chunk at %s failed after 6 tries: %w", clock(start), err)` where `clock` gives `m:ss`.
- The first fatal error cancels the other workers and is the returned error.
- Drop a text when `strings.TrimSpace` is empty or `transcribe.Hallucination` is true.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/filetx/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/filetx
git commit -m "Add filetx to transcribe a PCM stream in chunks"
```

### Task 3: The `oat transcribe` command

**Files:**
- Create: `cmd/oat/transcribe.go`
- Modify: `cmd/oat/main.go` (dispatch `transcribe`, usage text)
- Modify: `internal/doctor/doctor.go` (optional `ffmpeg` row), `cmd/oat/main.go` doctor deps
- Test: `cmd/oat/main_test.go`, `internal/doctor/doctor_test.go`

**Interfaces:**
- Consumes: `filetx.Transcribe`, `newClient`, `config.Load`, `config.Overrides`, `claudestt.MaxStreams`.
- Produces: `var ffmpegPath = "ffmpeg"` in `cmd/oat` (tests point it at a script); `doctor.Deps.FFmpeg func() (string, error)`.

Usage lines to add, exactly:

```
  oat transcribe FILE  print the text of an audio file

Flags for oat transcribe:
  --transcriber S  claude or groq
  --lang CODE      pt, en, or auto
  --model NAME     Groq only
  --keyterms LIST  names and acronyms, separated by commas
```

- [ ] **Step 1: Write the failing tests**

```go
func TestTranscribeNeedsOneFile(t *testing.T)        // run([]string{"transcribe"}) == 2, stderr has the usage
func TestTranscribeMissingFile(t *testing.T)         // a path that does not exist: exit 1, stderr names the path, stdout empty
func TestTranscribeWithoutFFmpeg(t *testing.T)       // ffmpegPath = "/nonexistent/ffmpeg": exit 1, stderr contains "ffmpeg was not found" and "sudo apt install ffmpeg"
func TestTranscribeFFmpegErrorGoesToStderr(t *testing.T) // script prints "Invalid data found" to stderr and exits 1: oat exits 1, stderr contains it, stdout empty
func TestTranscribePathWithSpaces(t *testing.T)      // script writes its $5 argument to a file; the file has "WhatsApp Ptt 2026 á.ogg" unchanged (script receives: -nostdin -v error -i PATH -f s16le -ac 1 -ar 16000 -)
func TestTranscribePrintsParagraphs(t *testing.T)    // script emits PCM of two speech blocks; a fake client is injected through a package var newTranscribeClient; stdout == "one\n\ntwo\n"
```

In `doctor_test.go`: `TestFFmpegIsOptional` shows OK with the path when found, and a non-failing row with the fix "Install ffmpeg to use oat transcribe: sudo apt install ffmpeg" when missing. A missing `ffmpeg` does not change the result of `Print`.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./cmd/oat/ ./internal/doctor/ -run 'Transcribe|FFmpeg' -v`
Expected: FAIL

- [ ] **Step 3: Implement**

- `runTranscribe(args, stdout, stderr)`: flags with `parseInterspersed`, exactly one file. `os.Stat` the file first. Load config, apply overrides, `Validate`, `newTranscribeClient(cfg, paths)` (a package var that defaults to `newClient`).
- `exec.CommandContext(ctx, ffmpegPath, "-nostdin", "-v", "error", "-i", path, "-f", "s16le", "-ac", "1", "-ar", "16000", "-")`, stdout piped to `filetx.Transcribe`, stderr captured. `exec.ErrNotFound` maps to the missing-ffmpeg message.
- `ctx` from `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`.
- Workers: `claudestt.MaxStreams` for claude, 2 for groq. `Log` writes retries to stderr.
- Print only after success: paragraphs joined by a blank line, newline at the end. On any error: nothing on stdout, the error and the ffmpeg stderr on stderr, exit 1.
- Keyterms: split `--keyterms` on commas, trim each.

- [ ] **Step 4: Run the tests**

Run: `make test`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/oat internal/doctor
git commit -m "Add oat transcribe for audio files"
```

### Task 4: Doctor and setup know the plugin server

**Files:**
- Modify: `internal/doctor/setup.go`, `internal/doctor/doctor.go`
- Test: `internal/doctor/doctor_test.go` (the existing `TestSetup*` file)

**Interfaces:**
- Produces: `doctor.PluginServer = "plugin:oat:oat"`. `MCPStatus` returns `(MCPState, error)` with `type MCPState struct{ User, Plugin bool }`. `Deps.MCPStatus` changes to that signature.

- [ ] **Step 1: Write the failing tests**

The fake runner answers `claude mcp get oat` and `claude mcp get plugin:oat:oat` from a set.

```go
func TestSetupSkipsWhenThePluginIsInstalled(t *testing.T) // only plugin:oat:oat exists: no "mcp add" call, output says the plugin provides the server
func TestMCPStatusPluginOnly(t *testing.T)                // doctor row OK, Detail "provided by the oat plugin"
func TestMCPStatusUserOnly(t *testing.T)                  // doctor row OK, Detail "registered in Claude Code as oat"
func TestMCPStatusBoth(t *testing.T)                      // row not OK, Detail "registered twice: by oat setup and by the oat plugin", Fix "claude mcp remove --scope user oat"
func TestMCPStatusNone(t *testing.T)                      // row not OK, Fix "Install the oat plugin, or run: oat setup"
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/doctor/ -v`
Expected: FAIL

- [ ] **Step 3: Implement** the two `claude mcp get` probes, the four rows above, and the setup skip. Update the doctor deps in `cmd/oat/main.go`.

- [ ] **Step 4: Run the tests**

Run: `make test`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/doctor cmd/oat
git commit -m "Recognize the MCP server of the oat plugin in doctor and setup"
```

### Task 5: The plugin, the marketplace, and the README

**Files:**
- Create: `.claude-plugin/marketplace.json`, `plugin/.claude-plugin/plugin.json`, `plugin/.mcp.json`, `plugin/skills/transcribe/SKILL.md`
- Modify: `README.md`

**Interfaces:**
- Consumes: the `oat transcribe` flags from Task 3.

- [ ] **Step 1: Write the files**

- `marketplace.json`: `{"name": "oat", "owner": {"name": "matheuscamposmt"}, "plugins": [{"name": "oat", "source": "./plugin", "description": "Meeting notes and audio transcription with the Claude Code voice stream"}]}`.
- `plugin.json`: `name` `oat`, `version` `0.1.0`, the same description.
- `.mcp.json`: `{"mcpServers": {"oat": {"command": "oat", "args": ["mcp"]}}}`.
- `SKILL.md` frontmatter: `name: transcribe`, a description that triggers on "transcribe", "transcrever", "what does this audio say", and audio extensions (ogg, opus, m4a, mp3, wav, webm, flac), `allowed-tools: Bash`. Body, in short procedural steps:
  1. Find the file path in the arguments or the conversation. Quote it.
  2. Build the key terms from the conversation and the project: proper names, acronyms, product and system names. Leave out names with accents when the backend is Claude. At most about 30 terms.
  3. Run `oat transcribe --keyterms "<terms>" "<file>"`. Use `--lang auto` when the conversation is not in Portuguese.
  4. Show the text as written. Mark words that look wrong instead of fixing them silently.
  5. Offer to save the text as a `.txt` next to the audio.
  6. If oat is missing, give the install line from the README. If the error says the login expired, tell the user to open Claude Code again.
- README: a "Claude Code plugin" install section first (`/plugin marketplace add matheuscamposmt/oat`, `/plugin install oat@oat`, then `oat doctor`), `oat setup` as the path without the plugin, a warning to use one of the two, and a section on `oat transcribe` with the key-terms rules (ASCII only for Claude, accents fine for Groq).

- [ ] **Step 2: Validate**

Run: `claude plugin validate ./plugin && claude plugin validate .`
Expected: both report no errors.

- [ ] **Step 3: Commit**

```bash
git add .claude-plugin plugin README.md
git commit -m "Ship oat as a Claude Code plugin with a transcribe skill"
```

### Task 6: Acceptance and the switch on this machine

Run on this machine with the real audio. No code unless a check fails.

- [ ] **Step 1: Build and install the binary**

Run: `make build && install -Dm755 bin/oat ~/.local/bin/oat`
Expected: `oat version` prints the branch build. (`make install` would run `oat setup`; that is fine after Task 4.)

- [ ] **Step 2: The four checks of the spec**

```bash
F="/home/matheusdemattos/Downloads/WhatsApp Ptt 2026-11-24 at 11.57.06.ogg"
oat transcribe --keyterms "Entra ID,GST" "$F" > /tmp/oat-full.txt; echo "exit $?"
ffmpeg -v error -y -i "$F" -t 8 /tmp/oat-8s.ogg && oat transcribe /tmp/oat-8s.ogg
ffmpeg -v error -y -f lavfi -t 10 -i anullsrc=r=16000:cl=mono /tmp/oat-silence.wav && oat transcribe /tmp/oat-silence.wav; echo "exit $?"
```

Expected: exit 0; "Entra ID" appears twice in `/tmp/oat-full.txt`; every sentence of the one-stream reference (`ptt_keyterms.txt` in the session scratchpad) is present; the 8 s clip ends with "é a gente resolver"; silence prints nothing and exits 0.

- [ ] **Step 3: The skill from the branch**

Run: `claude --plugin-dir ./plugin`, then in the session: "transcreva o áudio do WhatsApp de hoje em Downloads, falamos de Entra ID e da GST".
Expected: the skill runs `oat transcribe` with key terms that include "Entra ID", and shows the text. Type `/` and note the real name of the `enhance` prompt of the plugin server. Fix the README name if it differs, and commit.

- [ ] **Step 4: Push the branch**

Run: `git push -u origin transcribe-file-plugin`
Expected: the branch exists on GitHub, so the marketplace can read it.

- [ ] **Step 5: Switch this machine to the plugin**

```bash
claude plugin marketplace add matheuscamposmt/oat#transcribe-file-plugin
claude plugin install oat@oat
claude mcp remove --scope user oat
rm -r ~/.claude/skills/transcribe-audio
oat doctor
```

Expected: `oat doctor` shows the MCP server row OK, "provided by the oat plugin". `claude mcp list` shows `plugin:oat:oat` only once and no plain `oat`. The `transcribe-audio` skill no longer exists.

- [ ] **Step 6: Open the pull request**

Run: `gh pr create --fill`
Expected: a PR link. Do not merge it.
