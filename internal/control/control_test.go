package control

import (
	"testing"
	"time"
)

func at(h, m int) time.Time { return time.Date(2026, 10, 27, h, m, 0, 0, time.UTC) }

func TestWindowWrapsPastMidnight(t *testing.T) {
	w, err := ParseWindow("22:00-08:00")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		h, m  int
		night bool
	}{{21, 59, false}, {22, 0, true}, {23, 30, true}, {0, 0, true}, {7, 59, true}, {8, 0, false}, {12, 0, false}} {
		if got := w.Contains(at(c.h, c.m)); got != c.night {
			t.Errorf("%02d:%02d night=%v, want %v", c.h, c.m, got, c.night)
		}
	}
	for _, bad := range []string{"22-08", "25:00-08:00", "22:00-08:61", ""} {
		if _, err := ParseWindow(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func newSafety() *Safety {
	return &Safety{MaxTemp: 95, StaleAfter: time.Minute, HotToHalt: 2 * time.Minute,
		ClearFor: 5 * time.Minute, Cooldown: 15 * time.Minute}
}

func TestSafetyTripsAndRecovers(t *testing.T) {
	s := newSafety()
	t0 := at(12, 0)
	if s.Update(t0, 92, t0) != OK {
		t.Fatal("92 C tripped")
	}
	if s.Update(t0.Add(2*time.Second), 96, t0.Add(2*time.Second)) != Hot {
		t.Fatal("96 C did not trip")
	}
	// cooling to 84 C (more than 10 under) for 5 minutes returns to OK
	for i := 1; i <= 151; i++ { // first clear reading at +4 s, the 5 minutes end at +304 s
		now := t0.Add(time.Duration(2+2*i) * time.Second)
		s.Update(now, 84, now)
	}
	if s.State() != OK {
		t.Fatalf("state %v after 5 min at 84 C, want ok", s.State())
	}
}

func TestSafetyHaltsRetriesThenStops(t *testing.T) {
	s := newSafety()
	t0 := at(12, 0)
	s.Update(t0, 97, t0)
	if s.Update(t0.Add(2*time.Minute), 97, t0.Add(2*time.Minute)) != Halted {
		t.Fatal("2 min above the limit did not halt")
	}
	// while halted no reading arrives; after the cool-down it retries in Hot
	t1 := t0.Add(2*time.Minute + 15*time.Minute)
	if s.Update(t1, -273, t0) != Hot {
		t.Fatalf("no retry after the cool-down: %v", s.State())
	}
	// the retry trips again for 2 minutes: halted a second time, then stopped for good
	s.Update(t1.Add(time.Minute), 97, t1.Add(time.Minute))
	if s.Update(t1.Add(2*time.Minute), 97, t1.Add(2*time.Minute)) != Halted {
		t.Fatal("second trip did not halt")
	}
	if s.Update(t1.Add(20*time.Minute), -273, t1) != Stopped {
		t.Fatalf("state %v after the second halt, want stopped", s.State())
	}
}

func TestSafetyStaleReadingsTrip(t *testing.T) {
	s := newSafety()
	t0 := at(12, 0)
	// a sensor that stopped reporting must not look cool
	if s.Update(t0.Add(61*time.Second), 70, t0) != Hot {
		t.Fatal("61 s without a reading did not trip")
	}
}
