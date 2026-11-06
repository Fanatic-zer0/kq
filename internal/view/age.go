package view

import (
	"fmt"
	"strings"
	"time"
)

// Age formats a creation timestamp the way kubectl does: 45s, 5m, 3h, 12d, or 2y.
func Age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return FormatDuration(time.Since(t))
}

func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dy", int(d.Hours()/24/365))
	}
}

func joinOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ",")
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func boolOrDash(value *bool) string {
	if value == nil {
		return "-"
	}
	if *value {
		return "true"
	}
	return "false"
}
