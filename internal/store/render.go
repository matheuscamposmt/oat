package store

import (
	"fmt"
)

// Clock formats seconds on the meeting clock as HH:MM:SS.
// A negative value gives 00:00:00.
func Clock(secIn float64) string {
	if secIn < 0 {
		return "00:00:00"
	}
	sec := int(secIn)
	seconds := sec % 60
	mins := (sec / 60) % 60
	hours := sec / 60 / 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, mins, seconds)
}
