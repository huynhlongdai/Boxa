package boxd

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Output shaping (spec M7): agents get the tail of stdout/stderr, a truncated
// flag, and a log_id for the full output when it is large. tokens_saved is an
// estimate (bytes/4) of what the agent did not have to read.

const (
	DefaultTailLines = 60
	MaxTailBytes     = 16 << 10 // per stream returned to the agent
	KeepLogBytes     = 8 << 10  // keep full log when output exceeds this
	MaxCaptureBytes  = 200 << 20
)

// capWriter writes to a file and stops (silently) after a byte cap.
type capWriter struct {
	f       *os.File
	n       int64
	dropped int64
}

func (w *capWriter) Write(p []byte) (int, error) {
	room := int64(MaxCaptureBytes) - w.n
	if room <= 0 {
		w.dropped += int64(len(p))
		return len(p), nil
	}
	q := p
	if int64(len(q)) > room {
		q = q[:room]
		w.dropped += int64(len(p) - len(q))
	}
	n, err := w.f.Write(q)
	w.n += int64(n)
	if err != nil {
		return n, err
	}
	return len(p), nil
}

// tailFile returns the last n lines (bounded by MaxTailBytes), whether
// anything was cut, and the total line count.
func tailFile(path string, n int) (string, bool, int) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, 0
	}
	defer f.Close()
	st, _ := f.Stat()
	size := st.Size()
	if size == 0 {
		return "", false, 0
	}
	totalLines := countLines(f)
	const window = 512 << 10
	start := size - window
	if start < 0 {
		start = 0
	}
	f.Seek(start, io.SeekStart)
	buf, _ := io.ReadAll(f)
	return tailBytes(buf, n, start > 0, totalLines)
}

func countLines(f *os.File) int {
	f.Seek(0, io.SeekStart)
	buf := make([]byte, 64<<10)
	n, last := 0, byte('\n')
	for {
		k, err := f.Read(buf)
		if k > 0 {
			n += bytes.Count(buf[:k], []byte{'\n'})
			last = buf[k-1]
		}
		if err != nil {
			break
		}
	}
	if last != '\n' {
		n++
	}
	return n
}

// tailBytes is the pure core of tailFile (exported for tests via Shape).
func tailBytes(buf []byte, n int, cutBefore bool, totalLines int) (string, bool, int) {
	if n <= 0 {
		n = DefaultTailLines
	}
	raw := strings.TrimRight(string(buf), "\n")
	s, compacted := Compact(raw)
	lines := strings.Split(s, "\n")
	truncated := cutBefore || compacted
	if len(lines) > n {
		lines = lines[len(lines)-n:]
		truncated = true
	}
	out := strings.Join(lines, "\n")
	if len(out) > MaxTailBytes {
		out = out[len(out)-MaxTailBytes:]
		if i := strings.IndexByte(out, '\n'); i >= 0 && i < 200 {
			out = out[i+1:]
		}
		truncated = true
	}
	if totalLines == 0 {
		totalLines = len(strings.Split(raw, "\n"))
	}
	return strings.ToValidUTF8(out, "\uFFFD"), truncated, totalLines
}

// ShapeBytes shapes an in-memory output (used by tests and small outputs).
func ShapeBytes(out []byte, n int) (tail string, truncated bool) {
	t, tr, _ := tailBytes(out, n, false, 0)
	if len(out) == 0 {
		return "", false
	}
	return t, tr
}

// tokensSaved estimates tokens not sent to the agent.
func tokensSaved(totalBytes int64, returned int) int {
	d := totalBytes - int64(returned)
	if d <= 0 {
		return 0
	}
	return int(d / 4)
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\x1b[()][A-Z0-9]`)

// Compact (v1.3 exec fast path) removes noise an agent never needs to read:
// ANSI colour/cursor escapes, carriage-return progress redraws (only the
// final state of each line is kept) and runs of ≥3 identical lines, which
// become one line plus "[×N]". It reports whether anything was removed, so
// tokens_saved grows and the full log is kept.
func Compact(s string) (string, bool) {
	orig := len(s)
	if strings.IndexByte(s, 0x1b) >= 0 {
		s = ansiRe.ReplaceAllString(s, "")
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.IndexByte(l, '\r') >= 0 {
			parts := strings.Split(strings.TrimRight(l, "\r"), "\r")
			l = parts[len(parts)-1]
		}
		out = append(out, l)
	}
	res := make([]string, 0, len(out))
	for i := 0; i < len(out); {
		j := i + 1
		for j < len(out) && out[j] == out[i] {
			j++
		}
		if n := j - i; n >= 3 {
			res = append(res, out[i]+"  [×"+strconv.Itoa(n)+"]")
		} else {
			res = append(res, out[i:j]...)
		}
		i = j
	}
	s = strings.Join(res, "\n")
	return s, len(s) < orig
}
