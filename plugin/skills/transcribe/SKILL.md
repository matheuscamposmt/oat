---
name: transcribe
description: Transcribe a local audio file to text with oat and the Claude Code voice stream. Use when the user asks to transcribe or "transcrever" an audio, a voice message, or a recording, asks "what does this audio say", or points to an audio file (ogg, opus, m4a, mp3, wav, webm, flac).
allowed-tools: Bash
---

# Transcribe an audio file

1. Find the path of the audio file in the arguments or in the conversation. If the user names a folder and a time, such as "the WhatsApp audio of today in Downloads", list the folder and pick the file. Quote the path, because it can have spaces.
2. Build the key terms from the conversation and the project: proper names, acronyms, product names, and system names. The default backend is Claude, and it drops accented letters from the terms, so leave out names with accents. With `--transcriber groq`, accents are fine. Use at most about 30 terms.
3. Run:

   ```sh
   oat transcribe --keyterms "Term one,Term two" "/path/to/file.ogg"
   ```

   Add `--lang auto` when the conversation is not in Portuguese.
4. Show the text as oat prints it. If a word looks wrong, mark it, for example "Entrid [Entra ID?]". Do not fix it silently.
5. Offer to save the text as a `.txt` file next to the audio, with the same name.

## Errors

- `oat: command not found`: oat is not installed. Give the user the install lines from the README:

  ```sh
  curl -fsSL -o ~/.local/bin/oat https://github.com/matheuscamposmt/oat/releases/latest/download/oat-linux-amd64
  chmod +x ~/.local/bin/oat
  ```

- `ffmpeg was not found`: give the fix that oat prints.
- The error says that the login expired: tell the user to open Claude Code again, which renews the login, and then run the command again.
