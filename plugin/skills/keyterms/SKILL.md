---
name: keyterms
description: Manage the oat key term lists, the names and acronyms that oat sends to the speech-to-text backend so that it spells them right. Use when the user asks oat to learn the names of the current project, asks to scan the project for key terms, or says that a transcription or a meeting got a name wrong, for example "RAIS came out as raiz" or "o Entra ID saiu Enter ID".
allowed-tools: Bash
---

# Key term lists

oat keeps named lists in `~/.config/oat/config.toml`. A meeting uses the list whose name is in its title, or the list of `--keyterms-set`. Run `oat keyterms list` to see the lists and their folders.

## Scan the current project

1. Tell the user that Claude reads the descriptive files of the project (README, CLAUDE.md, AGENTS.md, docs), the same as in a Claude Code session there, and ask before you go on.
2. Run the scan. The list name defaults to the folder name. Add `--name NAME` when the user wants another name, for example the name they put in meeting titles. The scan asks two questions on stdin: the numbers to remove, then the confirmation. The Bash tool has no terminal, so answer them in the pipe:

   ```sh
   printf '\ny\n' | oat keyterms scan "$PWD" --name NAME
   ```

   The scan can take a few minutes.
3. Show the list from the output, and ask which terms to remove. Remove them with:

   ```sh
   oat keyterms rm NAME "Term one" "Term two"
   ```

## Fix a name after a wrong transcription

1. Find the list: the one whose folder is the current project in `oat keyterms list`, or the one whose name the meeting title has. If none fits, ask the user, or use the project folder name for a new list.
2. Add the right spelling:

   ```sh
   oat keyterms add NAME RAIS "Entra ID"
   ```

   New terms go to the top of the list. Write them without accents: the Claude backend drops accented letters, so "Vinícius" becomes "Vincius". Write "Vinicius".
3. Run `oat keyterms list NAME`, and tell the user about any warning it prints. The next recordings use the term. A meeting that was already recorded does not change.
