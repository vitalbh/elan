package google

import (
	"sort"
	"time"
)

// WorkHours are the working hours of a day, in minutes after midnight.
type WorkHours struct {
	Start, End int
}

// DefaultWorkHours is 09:00–19:00.
var DefaultWorkHours = WorkHours{Start: 9 * 60, End: 19 * 60}

// Slot is a free window.
type Slot struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Minutes is the length of the window.
func (s Slot) Minutes() int { return int(s.End.Sub(s.Start) / time.Minute) }

// slotRound aligns the start of a free window.
const slotRound = 5 * time.Minute

// FreeSlots returns the free windows of at least min inside the working
// hours of each weekday between from and to (days counted in from's
// location), around the events that block time (Event.Blocks). Starts are
// rounded up to 5 minutes.
func FreeSlots(events []Event, from, to time.Time, min time.Duration, hours WorkHours) []Slot {
	var busy []Slot
	for _, e := range events {
		if e.Blocks() {
			busy = append(busy, Slot{Start: e.Start, End: e.End})
		}
	}
	busy = merge(busy)
	loc := from.Location()
	var out []Slot
	day := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	for ; day.Before(to); day = day.AddDate(0, 0, 1) {
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		start := day.Add(time.Duration(hours.Start) * time.Minute)
		end := day.Add(time.Duration(hours.End) * time.Minute)
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		for _, w := range subtract(Slot{Start: start, End: end}, busy) {
			w.Start = roundUp(w.Start, day, slotRound)
			if w.End.Sub(w.Start) >= min && w.End.After(w.Start) {
				out = append(out, w)
			}
		}
	}
	return out
}

// merge sorts intervals and merges the overlapping or touching ones.
func merge(in []Slot) []Slot {
	if len(in) == 0 {
		return nil
	}
	s := append([]Slot(nil), in...)
	sort.Slice(s, func(i, j int) bool { return s[i].Start.Before(s[j].Start) })
	out := []Slot{s[0]}
	for _, b := range s[1:] {
		last := &out[len(out)-1]
		if !b.Start.After(last.End) {
			if b.End.After(last.End) {
				last.End = b.End
			}
			continue
		}
		out = append(out, b)
	}
	return out
}

// subtract removes the busy intervals (sorted, merged) from w.
func subtract(w Slot, busy []Slot) []Slot {
	if !w.End.After(w.Start) {
		return nil
	}
	var out []Slot
	cur := w.Start
	for _, b := range busy {
		if !b.End.After(cur) || !b.Start.Before(w.End) {
			continue
		}
		if b.Start.After(cur) {
			out = append(out, Slot{Start: cur, End: b.Start})
		}
		if b.End.After(cur) {
			cur = b.End
		}
	}
	if w.End.After(cur) {
		out = append(out, Slot{Start: cur, End: w.End})
	}
	return out
}

// roundUp aligns t up to a multiple of step counted from midnight.
func roundUp(t, midnight time.Time, step time.Duration) time.Time {
	off := t.Sub(midnight)
	if r := off % step; r != 0 {
		off += step - r
	}
	return midnight.Add(off)
}
