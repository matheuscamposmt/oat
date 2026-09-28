package store

import (
	"sort"
	"strings"
)

const (
	echoWindow   = 5.0 // seconds around a "Me" segment
	echoMinWords = 3
	echoRatio    = 0.7
	joinGap      = 2.0 // seconds
)

// Clean returns the transcript to show: sorted by time, without echo copies,
// and with consecutive segments of one speaker joined.
func Clean(segs []Segment) []Segment {
	var in []Segment
	for _, s := range segs {
		s.Text = strings.TrimSpace(s.Text)
		if s.Text != "" {
			in = append(in, s)
		}
	}
	sort.SliceStable(in, func(i, j int) bool { return in[i].Start < in[j].Start })

	var out []Segment
	for _, s := range in {
		if s.Speaker == Me && isEcho(s, in) {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Speaker == s.Speaker && s.Start-out[n-1].End < joinGap {
			out[n-1].Text += " " + s.Text
			out[n-1].End = max(out[n-1].End, s.End)
			continue
		}
		out = append(out, s)
	}
	return out
}

// isEcho reports whether a "Me" segment repeats the words of "Them" segments
// near it in time. On speakers, the mic records the remote voices too.
func isEcho(me Segment, all []Segment) bool {
	words := Words(me.Text)
	if len(words) < echoMinWords {
		return false
	}
	them := map[string]bool{}
	for _, s := range all {
		if s.Speaker != Them || s.Start > me.End+echoWindow || s.End < me.Start-echoWindow {
			continue
		}
		for _, w := range Words(s.Text) {
			them[w] = true
		}
	}
	hit := 0
	for _, w := range words {
		if them[w] {
			hit++
		}
	}
	return float64(hit)/float64(len(words)) >= echoRatio
}
