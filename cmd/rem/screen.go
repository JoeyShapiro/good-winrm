package main

import (
	"fmt"
	"io"
	"sync"
	"time"

	"good-winrm/lineedit"
)

// screen serializes writes to the terminal, and tracks whether the cursor
// is at the start of a line so system messages never start mid-line.
type screen struct {
	mu   sync.Mutex
	w    io.Writer
	crlf lineedit.CRLF
	last byte
}

// raw writes the connection's output untouched.
func (s *screen) raw(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.write(p)
}

// Write is for the meta terminal's own output, which needs "\r\n" in raw
// mode.
func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.write(s.crlf.Convert(p))
	return len(p), nil
}

// log prints a timestamped system message on its own line.
func (s *screen) log(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last != '\n' {
		s.write([]byte("\r\n"))
	}
	msg := fmt.Sprintf(format, args...)
	s.write([]byte(fmt.Sprintf("\033[33m[rem %s] %s\033[0m\r\n", time.Now().Format("15:04:05"), msg)))
}

func (s *screen) write(p []byte) {
	if len(p) == 0 {
		return
	}
	s.w.Write(p)
	s.last = p[len(p)-1]
}
