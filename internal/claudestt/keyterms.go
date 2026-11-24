package claudestt

import "strings"

// maxKeyterms is the length limit of the header in Claude Code.
const maxKeyterms = 1024

// EncodeKeyterms gives the x-config-keyterms header with the rules of Claude
// Code. Commas become spaces and non-ASCII characters are dropped. A term that
// does not fit in maxKeyterms ends the list.
func EncodeKeyterms(terms []string) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, t := range terms {
		t = strings.Map(func(r rune) rune {
			if r == ',' {
				return ' '
			}
			if r > 127 {
				return -1
			}
			return r
		}, t)
		t = strings.Join(strings.Fields(t), " ")
		if t == "" || seen[t] {
			continue
		}
		n := len(t)
		if b.Len() > 0 {
			n++
		}
		if b.Len()+n > maxKeyterms {
			break
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(t)
		seen[t] = true
	}
	return b.String()
}
