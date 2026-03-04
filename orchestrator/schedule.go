package orchestrator

import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// nextScheduleTime parses simple schedule expressions and returns the next run time.
// Supported formats:
//   - "every 5m" / "every 1h" / "every 30s" - interval-based
//   - "daily 09:00" - daily at a specific time
//   - "hourly" - every hour on the hour
func nextScheduleTime(schedule string) time.Time {
	now := time.Now()
	schedule = strings.TrimSpace(strings.ToLower(schedule))

	if strings.HasPrefix(schedule, "every ") {
		durStr := strings.TrimPrefix(schedule, "every ")
		dur, err := time.ParseDuration(durStr)
		if err == nil {
			return now.Add(dur)
		}
	}

	if strings.HasPrefix(schedule, "daily ") {
		timeStr := strings.TrimPrefix(schedule, "daily ")
		parts := strings.Split(timeStr, ":")
		if len(parts) == 2 {
			hour, _ := strconv.Atoi(parts[0])
			minute, _ := strconv.Atoi(parts[1])
			next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
			if next.Before(now) {
				next = next.Add(24 * time.Hour)
			}
			return next
		}
	}

	if schedule == "hourly" {
		next := time.Date(now.Year(), now.Month(), now.Day(), now.Hour()+1, 0, 0, 0, now.Location())
		return next
	}

	// Default: 1 hour from now
	return now.Add(time.Hour)
}

func generateID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
