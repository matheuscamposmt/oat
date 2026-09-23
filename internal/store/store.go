// Package store reads and writes the meeting folders.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Speaker is the stream that a segment comes from.
type Speaker string

const (
	Me   Speaker = "me"
	Them Speaker = "them"
)

// Label returns the name that the transcript shows.
func (s Speaker) Label() string {
	if s == Me {
		return "Me"
	}
	return "Them"
}

// Status is the state of a meeting.
type Status string

const (
	Recording   Status = "recording"
	Processing  Status = "processing"
	Done        Status = "done"
	Interrupted Status = "interrupted"
)

// Meta is the content of meta.json.
type Meta struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	Lang      string    `json:"lang"`
	Model     string    `json:"model"`
	Echo      string    `json:"echo"`
	Status    Status    `json:"status"`
}

// Duration returns the length of the meeting. A meeting without an end uses now.
func (m Meta) Duration(now time.Time) time.Duration {
	end := m.EndedAt
	if end.IsZero() {
		end = now
	}
	if end.Before(m.StartedAt) {
		return 0
	}
	return end.Sub(m.StartedAt)
}

// Segment is one line of transcript.jsonl. Start and End are seconds on the meeting clock.
type Segment struct {
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Speaker Speaker `json:"speaker"`
	Text    string  `json:"text"`
}

// ErrNotFound means that no meeting has the ID.
var ErrNotFound = errors.New("meeting not found")

// ErrNoLive means that no meeting records now.
var ErrNoLive = errors.New("no recording in progress")

const (
	metaFile       = "meta.json"
	segmentsFile   = "transcript.jsonl"
	transcriptFile = "transcript.md"
	notesFile      = "notes.md"
	summaryFile    = "summary.md"
	lockFile       = ".lock"
	chunksDir      = "chunks"
	failedDir      = "failed"
	audioDir       = "audio"
)

// Store is the folder that holds all meeting folders.
type Store struct {
	Root string
}

// New returns a Store for the folder root.
func New(root string) *Store { return &Store{Root: root} }

// Meeting is one meeting folder. Its methods are safe for concurrent use.
type Meeting struct {
	Dir  string
	Meta Meta // read it with Snapshot when other goroutines can change it
	mu   sync.Mutex
}

// DefaultTitle is the title of a meeting that has no title.
func DefaultTitle(t time.Time) string { return "Meeting " + t.Format("15:04") }

// Create makes a new meeting folder, takes its lock, and writes meta.json.
func (s *Store) Create(title string, start time.Time, lang, model, echo string) (*Meeting, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = DefaultTitle(start)
	}
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return nil, err
	}
	base := start.Format("2006-01-02-1504") + "-" + Slug(title)
	id := base
	for n := 2; ; n++ {
		err := os.Mkdir(filepath.Join(s.Root, id), 0o755)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
	m := &Meeting{
		Dir:  filepath.Join(s.Root, id),
		Meta: Meta{ID: id, Title: title, StartedAt: start, Lang: lang, Model: model, Echo: echo, Status: Recording},
	}
	if err := os.MkdirAll(m.ChunksDir(), 0o755); err != nil {
		return nil, err
	}
	if err := m.Lock(); err != nil {
		return nil, err
	}
	if err := m.SaveMeta(); err != nil {
		return nil, err
	}
	return m, nil
}

// Load reads one meeting. It marks a recording without a live process as interrupted.
func (s *Store) Load(id string) (*Meeting, error) {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	m := &Meeting{Dir: filepath.Join(s.Root, id)}
	data, err := os.ReadFile(m.path(metaFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &m.Meta); err != nil {
		return nil, fmt.Errorf("read %s: %w", m.path(metaFile), err)
	}
	m.Meta.ID = id
	if (m.Meta.Status == Recording || m.Meta.Status == Processing) && !m.IsLive() {
		m.Meta.Status = Interrupted
		_ = m.SaveMeta()
	}
	return m, nil
}

// List returns all meetings, newest first. It skips folders without a readable meta.json.
func (s *Store) List() ([]*Meeting, error) {
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Meeting
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if m, err := s.Load(e.Name()); err == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Meta.StartedAt.After(out[j].Meta.StartedAt) })
	return out, nil
}

// Latest returns the newest meeting.
func (s *Store) Latest() (*Meeting, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("%w: no meetings yet", ErrNotFound)
	}
	return all[0], nil
}

// Live returns the meeting that a live process records now.
func (s *Store) Live() (*Meeting, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, m := range all {
		if m.Meta.Status == Recording && m.IsLive() {
			return m, nil
		}
	}
	return nil, ErrNoLive
}

// Delete removes a meeting folder. It refuses a meeting that a live process uses.
func (s *Store) Delete(id string) error {
	m, err := s.Load(id)
	if err != nil {
		return err
	}
	if m.IsLive() {
		return fmt.Errorf("meeting %s is in use by process %d", id, m.LockPID())
	}
	return os.RemoveAll(m.Dir)
}

func (m *Meeting) path(name string) string { return filepath.Join(m.Dir, name) }

// ChunksDir holds the WAV files that wait for Groq.
func (m *Meeting) ChunksDir() string { return m.path(chunksDir) }

// FailedDir holds the WAV files that Groq refused.
func (m *Meeting) FailedDir() string { return m.path(failedDir) }

// AudioDir holds the WAV files that keep_audio keeps after transcription.
func (m *Meeting) AudioDir() string { return m.path(audioDir) }

// SummaryPath is the path of summary.md.
func (m *Meeting) SummaryPath() string { return m.path(summaryFile) }

// Snapshot returns a copy of the metadata.
func (m *Meeting) Snapshot() Meta {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Meta
}

// Update changes the metadata and writes meta.json.
func (m *Meeting) Update(fn func(*Meta)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(&m.Meta)
	data, err := json.MarshalIndent(m.Meta, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(m.path(metaFile), append(data, '\n'))
}

// SaveMeta writes meta.json.
func (m *Meeting) SaveMeta() error { return m.Update(func(*Meta) {}) }

// AppendSegments adds segments to transcript.jsonl with one write call.
func (m *Meeting) AppendSegments(segs []Segment) error {
	if len(segs) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, s := range segs {
		line, err := json.Marshal(s)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := os.OpenFile(m.path(segmentsFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Segments reads transcript.jsonl. It ignores a last line without a newline.
func (m *Meeting) Segments() ([]Segment, error) {
	data, err := os.ReadFile(m.path(segmentsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	i := bytes.LastIndexByte(data, '\n')
	if i < 0 {
		return nil, nil
	}
	var out []Segment
	for _, line := range bytes.Split(data[:i], []byte{'\n'}) {
		var s Segment
		if len(bytes.TrimSpace(line)) == 0 || json.Unmarshal(line, &s) != nil {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// WriteNotes replaces notes.md.
func (m *Meeting) WriteNotes(text string) error {
	return WriteFileAtomic(m.path(notesFile), []byte(text))
}

// Notes returns the content of notes.md, or "" when it does not exist.
func (m *Meeting) Notes() (string, error) { return readOptional(m.path(notesFile)) }

// WriteSummary replaces summary.md.
func (m *Meeting) WriteSummary(md string) error {
	return WriteFileAtomic(m.path(summaryFile), []byte(md))
}

// Summary returns the content of summary.md, or "" when it does not exist.
func (m *Meeting) Summary() (string, error) { return readOptional(m.path(summaryFile)) }

// HasNotes reports whether notes.md has text.
func (m *Meeting) HasNotes() bool {
	s, _ := m.Notes()
	return strings.TrimSpace(s) != ""
}

// HasSummary reports whether summary.md has text.
func (m *Meeting) HasSummary() bool {
	s, _ := m.Summary()
	return strings.TrimSpace(s) != ""
}

// Lock writes the PID of this process to .lock. It fails when another live process holds it.
func (m *Meeting) Lock() error {
	if pid := m.LockPID(); pid != 0 && pid != os.Getpid() && PIDAlive(pid) {
		return fmt.Errorf("meeting %s is in use by process %d", filepath.Base(m.Dir), pid)
	}
	return WriteFileAtomic(m.path(lockFile), []byte(strconv.Itoa(os.Getpid())))
}

// Unlock removes .lock.
func (m *Meeting) Unlock() error {
	err := os.Remove(m.path(lockFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// LockPID returns the PID in .lock, or 0.
func (m *Meeting) LockPID() int {
	data, err := os.ReadFile(m.path(lockFile))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

// IsLive reports whether a live process holds the lock.
func (m *Meeting) IsLive() bool {
	pid := m.LockPID()
	return pid != 0 && PIDAlive(pid)
}

// PIDAlive reports whether a process with the PID exists.
func PIDAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// ChunkName returns the file name of a chunk: the speaker and the start in milliseconds.
func ChunkName(sp Speaker, start float64) string {
	return fmt.Sprintf("%s-%09d.wav", sp, int64(math.Round(start*1000)))
}

// ParseChunkName reads the speaker and the start from a chunk file name.
func ParseChunkName(name string) (Speaker, float64, bool) {
	base := filepath.Base(name)
	if !strings.HasSuffix(base, ".wav") {
		return "", 0, false
	}
	sp, rest, ok := strings.Cut(strings.TrimSuffix(base, ".wav"), "-")
	if !ok || (Speaker(sp) != Me && Speaker(sp) != Them) {
		return "", 0, false
	}
	ms, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || ms < 0 {
		return "", 0, false
	}
	return Speaker(sp), float64(ms) / 1000, true
}

// PendingChunks lists the chunk files that wait for Groq, oldest first.
func (m *Meeting) PendingChunks() ([]string, error) {
	entries, err := os.ReadDir(m.ChunksDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type item struct {
		path  string
		start float64
	}
	var items []item
	for _, e := range entries {
		if _, start, ok := ParseChunkName(e.Name()); ok {
			items = append(items, item{filepath.Join(m.ChunksDir(), e.Name()), start})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].start < items[j].start })
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.path
	}
	return out, nil
}

// WriteFileAtomic writes data to a temporary file in the same folder, then renames it.
// A reader never sees a partial file.
func WriteFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func readOptional(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(data), err
}
