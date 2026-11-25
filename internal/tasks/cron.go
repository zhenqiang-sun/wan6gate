package tasks

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type CronSchedule struct {
	expr    string
	minutes map[int]bool
	hours   map[int]bool
	doms    map[int]bool
	months  map[int]bool
	dows    map[int]bool
}

// ParseCron parses a standard 5-field cron expression:
// minute (0-59), hour (0-23), day of month (1-31), month (1-12), day of week (0-7, 0 or 7 = Sun)
func ParseCron(expr string) (*CronSchedule, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron expression must have 5 fields, got %d in %q", len(fields), expr)
	}

	minutes, err := parseField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("invalid minute field %q: %w", fields[0], err)
	}
	hours, err := parseField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("invalid hour field %q: %w", fields[1], err)
	}
	doms, err := parseField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("invalid day of month field %q: %w", fields[2], err)
	}
	months, err := parseField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("invalid month field %q: %w", fields[3], err)
	}
	dows, err := parseField(fields[4], 0, 7)
	if err != nil {
		return nil, fmt.Errorf("invalid day of week field %q: %w", fields[4], err)
	}
	// normalize 7 (Sunday) to 0
	if dows[7] {
		dows[0] = true
		delete(dows, 7)
	}

	return &CronSchedule{
		expr:    expr,
		minutes: minutes,
		hours:   hours,
		doms:    doms,
		months:  months,
		dows:    dows,
	}, nil
}

func parseField(field string, min, max int) (map[int]bool, error) {
	res := make(map[int]bool)
	parts := strings.Split(field, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty item")
		}

		// check for step '/'
		step := 1
		rangePart := part
		if strings.Contains(part, "/") {
			sub := strings.Split(part, "/")
			if len(sub) != 2 {
				return nil, fmt.Errorf("invalid step syntax: %q", part)
			}
			rangePart = sub[0]
			s, err := strconv.Atoi(sub[1])
			if err != nil || s <= 0 {
				return nil, fmt.Errorf("invalid step value: %q", sub[1])
			}
			step = s
		}

		var start, end int
		if rangePart == "*" {
			start = min
			end = max
		} else if strings.Contains(rangePart, "-") {
			sub := strings.Split(rangePart, "-")
			if len(sub) != 2 {
				return nil, fmt.Errorf("invalid range syntax: %q", rangePart)
			}
			s, err1 := strconv.Atoi(sub[0])
			e, err2 := strconv.Atoi(sub[1])
			if err1 != nil || err2 != nil || s < min || e > max || s > e {
				return nil, fmt.Errorf("invalid range: %q (bounds [%d, %d])", rangePart, min, max)
			}
			start = s
			end = e
		} else {
			val, err := strconv.Atoi(rangePart)
			if err != nil || val < min || val > max {
				return nil, fmt.Errorf("value %q out of bounds [%d, %d]", rangePart, min, max)
			}
			if step > 1 {
				start = val
				end = max
			} else {
				res[val] = true
				continue
			}
		}

		for i := start; i <= end; i += step {
			res[i] = true
		}
	}

	if len(res) == 0 {
		return nil, fmt.Errorf("no matching values in range for %q", field)
	}
	return res, nil
}

// Matches returns true if the schedule matches the given time.
func (s *CronSchedule) Matches(t time.Time) bool {
	if !s.minutes[t.Minute()] {
		return false
	}
	if !s.hours[t.Hour()] {
		return false
	}
	if !s.doms[t.Day()] {
		return false
	}
	if !s.months[int(t.Month())] {
		return false
	}
	if !s.dows[int(t.Weekday())] {
		return false
	}
	return true
}

func (s *CronSchedule) String() string {
	return s.expr
}
