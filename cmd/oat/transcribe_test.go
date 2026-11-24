package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matheuscamposmt/oat/internal/chunk"
	"github.com/matheuscamposmt/oat/internal/claudestt"
	"github.com/matheuscamposmt/oat/internal/config"
	"github.com/matheuscamposmt/oat/internal/stt"
	"github.com/matheuscamposmt/oat/internal/transcribe"
)

// textBySeconds answers by the length of the chunk in whole seconds.
type textBySeconds map[int]string

func (f textBySeconds) Transcribe(ctx context.Context, wav []byte, opts stt.Options) (stt.Result, error) {
	return stt.Result{Text: f[int(chunk.WAVDuration(wav))]}, nil
}

// fakeFFmpeg points ffmpegPath at a shell script and injects client.
func fakeFFmpeg(t *testing.T, script string, client transcribe.Client) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath, oldClient := ffmpegPath, newTranscribeClient
	t.Cleanup(func() { ffmpegPath, newTranscribeClient = oldPath, oldClient })
	ffmpegPath = path
	newTranscribeClient = func(config.Config, config.Paths) (transcribe.Client, *claudestt.Client, error) {
		return client, nil, nil
	}
}

func audioFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// tone is a 440 Hz sine with one silent frame every 2 s, like speech with pauses.
func tone(secs int) []byte {
	n := secs * chunk.SampleRate
	b := make([]byte, 2*n)
	for i := range n {
		v := 4634 * math.Sin(2*math.Pi*440*float64(i)/chunk.SampleRate)
		if (i/chunk.FrameSamples)%100 == 99 {
			v = 0
		}
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(v)))
	}
	return b
}

func TestTranscribeNeedsOneFile(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"transcribe"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "oat transcribe FILE") {
		t.Fatalf("got %d %q", code, errOut.String())
	}
}

func TestTranscribeMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "none.ogg")
	var out, errOut bytes.Buffer
	if code := run([]string{"transcribe", path}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), path) || out.Len() != 0 {
		t.Fatalf("got %d %q %q", code, out.String(), errOut.String())
	}
}

func TestTranscribeWithoutFFmpeg(t *testing.T) {
	fakeFFmpeg(t, "", textBySeconds{})
	ffmpegPath = "/nonexistent/ffmpeg"
	var out, errOut bytes.Buffer
	code := run([]string{"transcribe", audioFile(t, "a.ogg")}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "ffmpeg was not found") || !strings.Contains(errOut.String(), "sudo apt install ffmpeg") {
		t.Fatalf("got %d %q", code, errOut.String())
	}
}

func TestTranscribeFFmpegErrorGoesToStderr(t *testing.T) {
	fakeFFmpeg(t, `echo "Invalid data found when processing input" >&2; exit 1`, textBySeconds{})
	var out, errOut bytes.Buffer
	code := run([]string{"transcribe", audioFile(t, "a.txt")}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "Invalid data found") || out.Len() != 0 {
		t.Fatalf("got %d %q %q", code, out.String(), errOut.String())
	}
}

func TestTranscribePathWithSpaces(t *testing.T) {
	got := filepath.Join(t.TempDir(), "arg")
	fakeFFmpeg(t, `printf %s "$5" > '`+got+`'`, textBySeconds{})
	path := audioFile(t, "WhatsApp Ptt 2026 á.ogg")
	var out, errOut bytes.Buffer
	if code := run([]string{"transcribe", path}, &out, &errOut); code != 0 {
		t.Fatalf("got %d %q", code, errOut.String())
	}
	if b, _ := os.ReadFile(got); string(b) != path {
		t.Fatalf("ffmpeg got %q, want %q", b, path)
	}
}

func TestTranscribePrintsParagraphs(t *testing.T) {
	pcm := filepath.Join(t.TempDir(), "pcm")
	if err := os.WriteFile(pcm, bytes.Join([][]byte{tone(11), make([]byte, 2*2*chunk.SampleRate), tone(13)}, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeFFmpeg(t, "cat '"+pcm+"'", textBySeconds{11: "one", 13: "two"})
	var out, errOut bytes.Buffer
	if code := run([]string{"transcribe", audioFile(t, "a.ogg")}, &out, &errOut); code != 0 || out.String() != "one\n\ntwo\n" {
		t.Fatalf("got %d %q %q", code, out.String(), errOut.String())
	}
}
