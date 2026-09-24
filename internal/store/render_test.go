package store

import "testing"

func TestClock(t *testing.T) {
	casos := []struct {
		sec  float64
		want string
	}{
		{754.9, "00:12:34"},
		{3725, "01:02:05"},
		{-3, "00:00:00"},
	}

	for _, c := range casos {
		if got := Clock(c.sec); got != c.want {
			t.Errorf("Clock(%v) = %q, want %q", c.sec, got, c.want)
		}
	}

}
