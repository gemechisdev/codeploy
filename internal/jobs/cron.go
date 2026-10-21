package jobs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed 5-field cron expression (minute hour day-of-month month day-of-week), evaluated at
// minute resolution in UTC.
//
// The daemon evaluates in UTC and does not consult TZ: this panel schedules host maintenance (backups,
// resource cron runs), not user-local events, and one zone for both storage and comparison keeps the
// firing logic free of daylight-saving surprises.
//
// The zero Schedule matches nothing; always go through Parse.
type Schedule struct {
	minute [60]bool
	hour   [24]bool
	dom    [31]bool // day d sets index d-1
	month  [12]bool // month m sets index m-1
	dow    [7]bool

	// domAll and dowAll record the bare "*" case, which dayMatches needs for the Vixie OR rule.
	// "*/2" is deliberately not "all": it is a restricted set even though it covers a lot.
	domAll bool
	dowAll bool

	expr string
}

// fieldSpec is the domain of one cron field. day-of-week accepts 7 as a Sunday alias, so dowLike fields
// validate against 0-7 but store against 0-6.
type fieldSpec struct {
	name    string
	min     int
	max     int
	dowLike bool
}

var cronFields = []fieldSpec{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day-of-month", min: 1, max: 31},
	{name: "month", min: 1, max: 12},
	{name: "day-of-week", min: 0, max: 6, dowLike: true},
}

// nextWindow bounds the search for a firing minute. Four years is the standard cron ceiling: it covers
// February 29 (which needs a leap cycle) and every expression that can never fire, like "0 0 30 2 *",
// simply runs out and returns ok=false. A caller can never spin.
const nextWindowYears = 4

// Parse validates a cron expression. Fields are separated by arbitrary runs of spaces or tabs; each field
// accepts "*", "*/n", "a", "a-b", "a-b/n", "a/n" and comma-separated lists of those. Names (mon, jan),
// "?" and other dialect extensions are rejected with an error naming the offending field.
func Parse(expr string) (Schedule, error) {
	if strings.ContainsAny(expr, "\n\r\v\f") {
		return Schedule{}, fmt.Errorf("cron: expression contains unsupported whitespace")
	}
	fields := strings.Fields(expr)
	if len(fields) != len(cronFields) {
		return Schedule{}, fmt.Errorf("cron: expression has %d fields, want %d (minute hour day-of-month month day-of-week)",
			len(fields), len(cronFields))
	}
	var s Schedule
	s.expr = strings.Join(fields, " ")
	for i, text := range fields {
		vals, bare, err := parseField(cronFields[i], text)
		if err != nil {
			return Schedule{}, err
		}
		switch i {
		case 0:
			for _, v := range vals {
				s.minute[v] = true
			}
		case 1:
			for _, v := range vals {
				s.hour[v] = true
			}
		case 2:
			for _, v := range vals {
				s.dom[v-1] = true
			}
			s.domAll = bare
		case 3:
			for _, v := range vals {
				s.month[v-1] = true
			}
		case 4:
			for _, v := range vals {
				s.dow[v] = true
			}
			s.dowAll = bare
		}
	}
	return s, nil
}

// Validate is the error-only form the HTTP handler uses for a 422 field error.
func Validate(expr string) error {
	_, err := Parse(expr)
	return err
}

// Next returns the first UTC minute strictly after "after" at which the schedule fires. ok=false means
// nothing matched inside a four-year window, so impossible schedules report absence instead of hanging.
func (s Schedule) Next(after time.Time) (time.Time, bool) {
	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(nextWindowYears, 0, 0)
	// Skip on the coarsest failing field. Stepping minute-by-minute across four years is two million
	// iterations; "0 0 30 2 *" bails after a few hundred day skips instead.
	for t.Before(limit) {
		if !s.month[int(t.Month())-1] {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !s.hour[t.Hour()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
			continue
		}
		if !s.minute[t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t, true
	}
	return time.Time{}, false
}

// Matches reports whether t (truncated to the minute) fires.
func (s Schedule) Matches(t time.Time) bool {
	t = t.UTC().Truncate(time.Minute)
	return s.minute[t.Minute()] && s.hour[t.Hour()] && s.month[int(t.Month())-1] && s.dayMatches(t)
}

// Describe returns the expression as normalized (single-space separated) text.
func (s Schedule) Describe() string { return s.expr }

// dayMatches implements Vixie cron's day rule, which surprises everyone: when BOTH day fields are
// restricted, the test is OR, not AND. "0 0 13 * 5" fires on every Friday and every the 13th, not only on
// Friday the 13th. We keep it because every other crontab on the host behaves this way and an operator
// transferring a schedule expects the same dates. When either field is a bare "*" the rule collapses to
// AND, which is indistinguishable from OR against a set that matches everything.
func (s Schedule) dayMatches(t time.Time) bool {
	dom := s.dom[t.Day()-1]
	dow := s.dow[int(t.Weekday())]
	if !s.domAll && !s.dowAll {
		return dom || dow
	}
	return dom && dow
}

// parseField expands one field into its values. bare reports a plain "*" field.
func parseField(f fieldSpec, text string) (vals []int, bare bool, err error) {
	if tok := unsupportedToken(text); tok != "" {
		return nil, false, fmt.Errorf("cron: %s field %q: %q is not supported (numeric fields only: no names, no ? L W #)",
			f.name, text, tok)
	}
	items := strings.Split(text, ",")
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, false, fmt.Errorf("cron: %s field %q: empty list item", f.name, text)
		}
		v, err := f.parseItem(item)
		if err != nil {
			return nil, false, err
		}
		vals = append(vals, v...)
	}
	return vals, len(items) == 1 && text == "*", nil
}

func (f fieldSpec) parseItem(item string) ([]int, error) {
	base, step := item, 1
	if n := strings.Count(item, "/"); n > 1 {
		return nil, fmt.Errorf("cron: %s field: %q has %d steps, want at most 1", f.name, item, n)
	}
	if i := strings.Index(item, "/"); i >= 0 {
		base = item[:i]
		n, err := strconv.Atoi(strings.TrimSpace(item[i+1:]))
		if err != nil {
			return nil, fmt.Errorf("cron: %s field: step %q in %q is not a number", f.name, item[i+1:], item)
		}
		if n <= 0 {
			return nil, fmt.Errorf("cron: %s field: step must be at least 1, got %d", f.name, n)
		}
		step = n
	}
	if base == "*" {
		return f.expand(f.min, f.max, step)
	}
	dash := strings.Index(base, "-")
	if dash < 0 {
		a, err := f.number(base)
		if err != nil {
			return nil, err
		}
		if step != 1 {
			// "a/n" means a..field-max stepping n, the same reading Vixie gives it.
			return f.expand(f.fold(a), f.max, step)
		}
		return []int{f.fold(a)}, nil
	}
	if strings.Count(base, "-") > 1 {
		return nil, fmt.Errorf("cron: %s field: %q has more than one range bound", f.name, base)
	}
	a, err := f.number(base[:dash])
	if err != nil {
		return nil, err
	}
	b, err := f.number(base[dash+1:])
	if err != nil {
		return nil, err
	}
	return f.expandRange(a, b, step)
}

// expandRange lists the values between two endpoints, applying the day-of-week Sunday alias and wrap.
func (f fieldSpec) expandRange(start, end, step int) ([]int, error) {
	if f.dowLike {
		// 0-7 is the whole week, not a wrap back to Sunday. "5-7" folds to 5-0 and reads Fri-Sun via
		// the wrap rule below.
		if start == 0 && end == 7 {
			return f.expand(0, 6, step)
		}
		if start == 7 {
			start = 0
		}
		if end == 7 {
			end = 0
		}
	}
	if start > end {
		if !f.dowLike {
			return nil, fmt.Errorf("cron: %s field: range %d-%d starts after it ends", f.name, start, end)
		}
		return f.expandWrap(start, end, step)
	}
	return f.expand(start, end, step)
}

// expandWrap runs day-of-week cyclically: "5-1" means Friday through Monday. Only this field wraps; a
// reversed range anywhere else is a typo, and the caller gets the error. The inclusive span around the
// cycle is (end - start + 7) % 7 + 1; start > end here, so it reduces to 8 + end - start.
func (f fieldSpec) expandWrap(start, end, step int) ([]int, error) {
	var vals []int
	for i := 0; i < 8+end-start; i += step {
		vals = append(vals, (start+i)%7)
	}
	return vals, nil
}

func (f fieldSpec) expand(start, end, step int) ([]int, error) {
	if start > end {
		return nil, fmt.Errorf("cron: %s field: range %d-%d starts after it ends", f.name, start, end)
	}
	var vals []int
	for v := start; v <= end; v += step {
		vals = append(vals, v)
	}
	return vals, nil
}

// number parses one endpoint and bounds-checks it against the field. day-of-week allows the 7 alias.
func (f fieldSpec) number(text string) (int, error) {
	text = strings.TrimSpace(text)
	v, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("cron: %s field: %q is not a number (month and day names are not supported)", f.name, text)
	}
	hi := f.max
	if f.dowLike {
		hi = 7
	}
	if v < f.min || v > hi {
		return 0, fmt.Errorf("cron: %s field: %d is out of range %d-%d", f.name, v, f.min, hi)
	}
	return v, nil
}

// fold maps the day-of-week Sunday alias onto its canonical value.
func (f fieldSpec) fold(v int) int {
	if f.dowLike && v == 7 {
		return 0
	}
	return v
}

// unsupportedToken names the first dialect extension in the text, so the error says exactly what to remove.
func unsupportedToken(text string) string {
	for _, r := range text {
		switch r {
		case '?', 'L', 'l', 'W', 'w', '#':
			return string(r)
		}
	}
	return ""
}
