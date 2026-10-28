// Package control decides, from the clock and the ASIC temperatures, which work level
// and fan mode nano3d should run: the owner's night/day schedule and the over-heat
// safety. It holds no hardware; cmd/nano3d applies its decisions.
package control

import (
	"fmt"
	"time"
)

// Window is a daily time range, such as 22:00-08:00 (it may wrap past midnight).
type Window struct{ from, to int } // minutes after midnight

// ParseWindow reads "HH:MM-HH:MM".
func ParseWindow(s string) (Window, error) {
	var h1, m1, h2, m2 int
	if _, err := fmt.Sscanf(s, "%d:%d-%d:%d", &h1, &m1, &h2, &m2); err != nil ||
		h1 > 23 || h2 > 23 || m1 > 59 || m2 > 59 || h1 < 0 || h2 < 0 || m1 < 0 || m2 < 0 {
		return Window{}, fmt.Errorf("bad time window %q (want HH:MM-HH:MM)", s)
	}
	return Window{from: h1*60 + m1, to: h2*60 + m2}, nil
}

// String is the window as ParseWindow reads it.
func (w Window) String() string {
	return fmt.Sprintf("%02d:%02d-%02d:%02d", w.from/60, w.from%60, w.to/60, w.to%60)
}

// Contains reports whether t (in its own location) falls in the window.
func (w Window) Contains(t time.Time) bool {
	m := t.Hour()*60 + t.Minute()
	if w.from <= w.to {
		return m >= w.from && m < w.to
	}
	return m >= w.from || m < w.to // wraps past midnight
}

// State of the over-heat safety.
type State int

const (
	OK      State = iota // follow the schedule, fan on its PID
	Hot                  // level 0, fan 100 %
	Halted               // K210 held in reset, fan 100 %
	Stopped              // halted for good after a second trip: needs a restart
)

func (s State) String() string {
	return [...]string{"ok", "hot", "halted", "stopped"}[s]
}

// Safety is the over-heat state machine. A trip is the hottest ASIC above MaxTemp, or
// no temperature reading for StaleAfter (a silent sensor must not look cool).
type Safety struct {
	MaxTemp    float32
	StaleAfter time.Duration // no reading for this long counts as a trip
	HotToHalt  time.Duration // still tripped this long in Hot: halt
	ClearFor   time.Duration // this long at least 10 C under MaxTemp in Hot: back to OK
	Cooldown   time.Duration // time halted before one retry at level 0

	state      State
	since      time.Time // entered the current state
	clearSince time.Time // zero while not clear
	halts      int
}

// State is the current safety state.
func (s *Safety) State() State { return s.state }

// Update feeds one reading and returns the state to apply now. lastReading is the time
// of the newest temperature report.
func (s *Safety) Update(now time.Time, hottest float32, lastReading time.Time) State {
	tripped := hottest > s.MaxTemp || now.Sub(lastReading) > s.StaleAfter
	clear := !tripped && hottest <= s.MaxTemp-10
	switch s.state {
	case OK:
		if tripped {
			s.enter(Hot, now)
		}
	case Hot:
		switch {
		case tripped:
			s.clearSince = time.Time{}
			if now.Sub(s.since) >= s.HotToHalt {
				s.halts++
				s.enter(Halted, now)
			}
		case clear:
			if s.clearSince.IsZero() {
				s.clearSince = now
			}
			if now.Sub(s.clearSince) >= s.ClearFor {
				s.enter(OK, now)
			}
		default:
			s.clearSince = time.Time{}
		}
	case Halted:
		// No readings arrive while the K210 is in reset; wait out the cool-down,
		// then retry once in Hot (level 0, fan 100 %).
		if now.Sub(s.since) >= s.Cooldown {
			if s.halts >= 2 {
				s.enter(Stopped, now)
			} else {
				s.enter(Hot, now)
			}
		}
	}
	return s.state
}

func (s *Safety) enter(st State, now time.Time) {
	s.state, s.since, s.clearSince = st, now, time.Time{}
}
