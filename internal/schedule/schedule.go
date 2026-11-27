package schedule

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Spec struct {
	Interval     time.Duration
	Hour, Minute int
	Days         []time.Weekday
}

var clock = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
var interval = regexp.MustCompile(`^[0-9]+[smh]$`)
var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func Parse(text string) (Spec, error) {
	var s Spec
	p := strings.Split(text, " ")
	if len(p) == 2 && p[0] == "every" && interval.MatchString(p[1]) {
		d, err := time.ParseDuration(p[1])
		if err == nil && d > 0 {
			s.Interval = d
			return s, nil
		}
	}
	if (len(p) == 2 && p[0] == "daily" || len(p) == 3 && p[0] == "weekly") && clock.MatchString(p[len(p)-1]) {
		t, _ := time.Parse("15:04", p[len(p)-1])
		s.Hour, s.Minute = t.Hour(), t.Minute()
		if p[0] == "weekly" {
			var seen [7]bool
			for _, day := range strings.Split(p[1], ",") {
				n := -1
				for i, d := range weekdays {
					if d == day {
						n = i
						break
					}
				}
				if n < 0 || seen[n] {
					return Spec{}, fmt.Errorf("invalid or duplicate weekday")
				}
				seen[n] = true
			}
			for i, yes := range seen {
				if yes {
					s.Days = append(s.Days, time.Weekday(i))
				}
			}
		}
		return s, nil
	}
	return s, fmt.Errorf("expected daily HH:MM, weekly mon,thu HH:MM, or every Ns/Nm/Nh")
}

// Next estimates the next configured occurrence, not the OS scheduler's state.
func (s Spec) Next(now, last time.Time) time.Time {
	if s.Interval > 0 {
		if last.IsZero() {
			return now.Add(s.Interval)
		}
		if last.After(now) {
			return last.Add(s.Interval).In(now.Location())
		}
		return now.Add(s.Interval - now.Sub(last)%s.Interval)
	}
	for i := 0; i <= 14; i++ {
		day := now.AddDate(0, 0, i)
		if len(s.Days) > 0 {
			matched := false
			for _, d := range s.Days {
				if day.Weekday() == d {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		y, m, d := day.Date()
		t := time.Date(y, m, d, s.Hour, s.Minute, 0, 0, now.Location())
		if t.Day() == d && t.Hour() == s.Hour && t.Minute() == s.Minute && t.After(now) {
			return t
		}
	}
	return time.Time{}
}
