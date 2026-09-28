package session

import (
	"context"
	"time"

	"github.com/matheuscamposmt/oat/internal/config"
	"github.com/matheuscamposmt/oat/internal/store"
	"github.com/matheuscamposmt/oat/internal/transcribe"
)

// DrainEvent reports the progress of a drain of leftover chunks.
type DrainEvent struct {
	ID      string
	Pending int
	Done    bool
}

// Drain sends the chunks that earlier runs left behind, one meeting at a time.
// It skips meetings that a live process uses.
func Drain(ctx context.Context, st *store.Store, client transcribe.Client, cfg config.Config, emit func(any)) {
	meetings, err := st.List()
	if err != nil {
		return
	}
	for _, m := range meetings {
		if ctx.Err() != nil {
			return
		}
		status := m.Snapshot().Status
		if (status != store.Interrupted && status != store.Processing) || m.IsLive() {
			continue
		}
		drainOne(ctx, m, client, cfg, emit)
	}
}

func drainOne(ctx context.Context, m *store.Meeting, client transcribe.Client, cfg config.Config, emit func(any)) {
	if err := m.Lock(); err != nil {
		return
	}
	defer m.Unlock()
	meta := m.Snapshot()
	_ = m.Update(func(mm *store.Meta) { mm.Status = store.Processing })
	pipe := transcribe.New(m, client, transcribe.Config{Model: meta.Model, KeepAudio: cfg.KeepAudio}, func(ev any) {
		if q, ok := ev.(transcribe.QueueEvent); ok {
			emit(DrainEvent{ID: meta.ID, Pending: q.Pending})
		}
	})
	pipe.Start(ctx)
	_, _ = pipe.EnqueuePending()
	pipe.Close()
	err := pipe.Wait()
	// A failed transcript write leaves the chunk on disk, so the meeting stays
	// processing, and the next drain sends the chunk again.
	left, lerr := m.PendingChunks()
	if err == nil && ctx.Err() == nil && pipe.Pending() == 0 && lerr == nil && len(left) == 0 {
		end := meta.EndedAt
		if end.IsZero() {
			end = lastEnd(m, meta.StartedAt)
		}
		_ = m.Update(func(mm *store.Meta) {
			mm.Status = store.Done
			mm.EndedAt = end
		})
	}
	_ = m.WriteTranscriptMD()
	emit(DrainEvent{ID: meta.ID, Pending: pipe.Pending(), Done: true})
}

// lastEnd estimates the end of an interrupted meeting from its last segment.
func lastEnd(m *store.Meeting, start time.Time) time.Time {
	segs, _ := m.Segments()
	var end float64
	for _, s := range segs {
		end = max(end, s.End)
	}
	return start.Add(time.Duration(end * float64(time.Second)))
}
