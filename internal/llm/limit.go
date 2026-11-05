package llm

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// limitMarker matches the messages the CLI prints when the subscription is
// out of quota, e.g. "You've hit your session limit · resets 2pm
// (America/Sao_Paulo)", "You've hit your weekly limit", "Claude AI usage
// limit reached|1730000000".
var limitMarker = regexp.MustCompile(`(?i)hit your (?:\w+[ -])*limit|usage limit|(?:session|weekly|daily|monthly|5-hour|opus|sonnet) limit|limit reached`)

// limitEpoch is the older "usage limit reached|<unix seconds>" format.
var limitEpoch = regexp.MustCompile(`limit reached\|(\d{9,})`)

// resetAt matches "resets 2pm", "resets 2:30pm (America/Sao_Paulo)",
// "resets at 14:00", "resets Nov 3, 9am (UTC)", "resets Nov 3 at 9am".
var resetAt = regexp.MustCompile(`(?i)resets\s+(?:at\s+)?(?:([a-z]{3,9})\.?\s+(\d{1,2})(?:,\s*|\s+at\s+|\s+))?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?(?:\s*\(([^)]+)\))?`)

// detectLimit returns a *LimitError when text carries a usage limit message.
func detectLimit(text string, now time.Time) *LimitError {
	loc := limitMarker.FindStringIndex(text)
	if loc == nil {
		return nil
	}
	return &LimitError{Message: limitLine(text, loc[0]), ResetAt: parseReset(text, now)}
}

// limitLine is the line of text holding the marker.
func limitLine(text string, at int) string {
	start := strings.LastIndexByte(text[:at], '\n') + 1
	end := strings.IndexByte(text[at:], '\n')
	if end < 0 {
		return strings.TrimSpace(text[start:])
	}
	return strings.TrimSpace(text[start : at+end])
}

var months = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April,
	"may": time.May, "jun": time.June, "jul": time.July, "aug": time.August,
	"sep": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
}

// parseReset finds when the limit resets. Zero when the text does not say.
// A bare time of day is the next occurrence after now.
func parseReset(text string, now time.Time) time.Time {
	if m := limitEpoch.FindStringSubmatch(text); m != nil {
		if sec, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return time.Unix(sec, 0)
		}
	}
	m := resetAt.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}
	}
	monthName, dayS, hourS, minS, ampm, tz := m[1], m[2], m[3], m[4], strings.ToLower(m[5]), m[6]
	loc := now.Location()
	if tz != "" {
		if l, err := time.LoadLocation(strings.TrimSpace(tz)); err == nil {
			loc = l
		}
	}
	hour, _ := strconv.Atoi(hourS)
	minute, _ := strconv.Atoi(minS)
	switch {
	case ampm == "" && hour > 23, ampm != "" && (hour < 1 || hour > 12), minute > 59:
		return time.Time{}
	case ampm == "am" && hour == 12:
		hour = 0
	case ampm == "pm" && hour < 12:
		hour += 12
	}
	local := now.In(loc)
	if monthName != "" {
		month, ok := months[strings.ToLower(monthName)[:3]]
		day, _ := strconv.Atoi(dayS)
		if !ok || day < 1 || day > 31 {
			return time.Time{}
		}
		t := time.Date(local.Year(), month, day, hour, minute, 0, 0, loc)
		if t.Before(local.AddDate(0, 0, -1)) { // "Jan 2" read in late December
			t = t.AddDate(1, 0, 0)
		}
		return t
	}
	t := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if !t.After(local) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}
