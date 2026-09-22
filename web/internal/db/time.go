package db

import (
	"strings"
	"time"
)

var BeijingLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

// BeijingTimestamp converts persisted UTC timestamps to an explicit Beijing offset.
func BeijingTimestamp(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.In(BeijingLocation).Format(time.RFC3339Nano)
		}
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return parsed.In(BeijingLocation).Format(time.RFC3339Nano)
		}
	}
	return raw
}
