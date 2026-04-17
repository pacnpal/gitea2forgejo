package dump

import (
	"bytes"
	"log/slog"
)

// logWriter adapts an io.Writer to slog; each line is emitted at Info.
// Useful for streaming stdout/stderr from long-running remote commands.
type logWriter struct {
	log    *slog.Logger
	prefix string
	buf    bytes.Buffer
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		i := bytes.IndexByte(w.buf.Bytes(), '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(w.buf.Next(i + 1))
		// Trim trailing newline; slog attaches its own.
		if n := len(line); n > 0 && line[n-1] == '\n' {
			line = line[:n-1]
		}
		w.log.Info(w.prefix, "msg", line)
	}
}
