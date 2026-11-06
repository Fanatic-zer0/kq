package view

import (
	"io"
	"os"
	"strings"
)

const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
)

// colorEnabled is set once per process invocation before any rendering occurs;
// kq is a single-shot CLI, so no synchronization is needed for this read-mostly flag.
var colorEnabled = false

// SetColorMode decides whether ANSI colors are emitted. Color is on by default
// ("auto"), honoring only the NO_COLOR convention; "always" forces color even
// with NO_COLOR set, and "never" disables it unconditionally.
func SetColorMode(mode string, w io.Writer) {
	switch mode {
	case "always":
		colorEnabled = true
	case "never":
		colorEnabled = false
	default:
		_, noColor := os.LookupEnv("NO_COLOR")
		colorEnabled = !noColor
	}
}

func colorize(code, text string) string {
	if !colorEnabled || text == "" {
		return text
	}
	return code + text + ansiReset
}

func bold(text string) string   { return colorize(ansiBold, text) }
func red(text string) string    { return colorize(ansiRed, text) }
func green(text string) string  { return colorize(ansiGreen, text) }
func yellow(text string) string { return colorize(ansiYellow, text) }
func cyan(text string) string   { return colorize(ansiCyan, text) }

// statusColor classifies a STATUS/STATE/READY-style cell for consistent coloring
// across list and detail views (green=healthy, yellow=in-progress, red=failing).
func statusColor(value string) string {
	switch strings.ToUpper(value) {
	case "RUNNING", "ACTIVE", "BOUND", "OK", "TRUE", "READY", "AVAILABLE":
		return green(value)
	case "COMPLETED", "SUCCEEDED":
		return cyan(value)
	case "PENDING", "CONTAINERCREATING", "WAITING", "PROGRESSING", "NOT FOUND", "OLD", "TERMINATING", "RELEASED":
		return yellow(value)
	case "ERROR", "FAILED", "CRASHLOOPBACKOFF", "IMAGEPULLBACKOFF", "ERRIMAGEPULL", "NO ACCESS", "ERROR_", "UNKNOWN", "FALSE", "EVICTED", "OOMKILLED":
		return red(value)
	default:
		return value
	}
}

func eventTypeColor(eventType string) string {
	switch eventType {
	case "Warning":
		return yellow(eventType)
	case "Normal":
		return green(eventType)
	default:
		return eventType
	}
}

// Table accumulates rows and renders them with color applied after width padding,
// so ANSI escape sequences (which are zero-width on a terminal) never shift alignment.
type Table struct {
	headers      []string
	rows         [][]string
	colorColumns map[int]bool
}

func NewTable(headers ...string) *Table {
	table := &Table{headers: headers, colorColumns: map[int]bool{}}
	for i, h := range headers {
		switch strings.ToUpper(h) {
		case "STATUS", "STATE", "READY":
			table.colorColumns[i] = true
		}
	}
	return table
}

func (t *Table) Row(cells ...string) {
	t.rows = append(t.rows, cells)
}

func (t *Table) Flush(w io.Writer) error {
	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = len(h)
	}
	for _, row := range t.rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	var b strings.Builder
	writeRow(&b, t.headers, widths, nil, true)
	for _, row := range t.rows {
		writeRow(&b, row, widths, t.colorColumns, false)
	}
	_, err := w.Write([]byte(b.String()))
	return err
}

func writeRow(b *strings.Builder, cells []string, widths []int, colorColumns map[int]bool, isHeader bool) {
	for i, cell := range cells {
		width := 0
		if i < len(widths) {
			width = widths[i]
		}
		pad := width - len(cell)
		if pad < 0 {
			pad = 0
		}
		display := cell
		if isHeader {
			display = bold(cell)
		} else if colorColumns[i] {
			display = statusColor(cell)
		}
		b.WriteString(display)
		if i < len(cells)-1 {
			for range pad {
				b.WriteByte(' ')
			}
			b.WriteString("  ")
		}
	}
	b.WriteString("\n")
}
