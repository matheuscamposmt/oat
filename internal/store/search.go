package store

import (
	"errors"
	"strings"
)

// Hit is one search result.
type Hit struct {
	MeetingID string
	Title     string
	At        float64 // -1 for a line in the notes
	Speaker   Speaker // empty for a line in the notes
	Text      string
}

// Search finds the lines of transcripts and notes that contain query, newest
// meetings first. It ignores case and accents.
func (s *Store) Search(query string, limit int) ([]Hit, error) {
	q := strings.TrimSpace(Fold(query))
	if q == "" {
		return nil, errors.New("query must not be empty")
	}
	if limit <= 0 {
		limit = 20
	}
	meetings, err := s.List()
	if err != nil {
		return nil, err
	}
	var hits []Hit
	for _, m := range meetings {
		segs, _ := m.Segments()
		for _, seg := range Clean(segs) {
			if strings.Contains(Fold(seg.Text), q) {
				hits = append(hits, Hit{m.Meta.ID, m.Meta.Title, seg.Start, seg.Speaker, seg.Text})
				if len(hits) >= limit {
					return hits, nil
				}
			}
		}
		notes, _ := m.Notes()
		for _, line := range strings.Split(notes, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && strings.Contains(Fold(line), q) {
				hits = append(hits, Hit{m.Meta.ID, m.Meta.Title, -1, "", line})
				if len(hits) >= limit {
					return hits, nil
				}
			}
		}
	}
	return hits, nil
}
