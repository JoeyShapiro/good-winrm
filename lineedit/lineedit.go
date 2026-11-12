// Package lineedit is a minimal line editor for raw-mode terminal input, for
// remotes that have no terminal of their own to do it.
package lineedit

import (
	"strings"
	"unicode/utf8"
)

const (
	keyCtrlC     = 0x03
	keyCtrlD     = 0x04
	keyCtrlH     = 0x08
	keyCtrlU     = 0x15
	keyEsc       = 0x1b
	keyBackspace = 0x7f
)

type EventKind int

const (
	// Line is a completed line (Enter), without the newline.
	Line EventKind = iota
	// Interrupt is Ctrl+C; the current line has been discarded.
	Interrupt
	// EOF is Ctrl+D on an empty line.
	EOF
)

type Event struct {
	Kind EventKind
	Line string
}

type Editor struct {
	line    []rune
	partial []byte // incomplete UTF-8 sequence from the previous Feed
}

// Feed processes raw terminal input, returning what to echo back to the
// terminal and the events it produced, in order.
func (e *Editor) Feed(p []byte) (echo []byte, events []Event) {
	p = append(e.partial, p...)
	e.partial = nil

	for len(p) > 0 {
		b := p[0]
		switch {
		case b == keyEsc:
			p = skipEscape(p)
			continue
		case b == keyCtrlC:
			e.line = nil
			echo = append(echo, "^C\r\n"...)
			events = append(events, Event{Kind: Interrupt})
		case b == keyCtrlD:
			if len(e.line) == 0 {
				events = append(events, Event{Kind: EOF})
			}
		case b == keyBackspace || b == keyCtrlH:
			if len(e.line) > 0 {
				e.line = e.line[:len(e.line)-1]
				echo = append(echo, "\b \b"...)
			}
		case b == keyCtrlU:
			echo = append(echo, strings.Repeat("\b \b", len(e.line))...)
			e.line = nil
		case b == '\r' || b == '\n':
			// Pasted text may use \r\n; treat it as a single Enter
			if b == '\r' && len(p) > 1 && p[1] == '\n' {
				p = p[1:]
			}
			echo = append(echo, "\r\n"...)
			events = append(events, Event{Kind: Line, Line: string(e.line)})
			e.line = nil
		case b < 0x20 && b != '\t':
			// Other control keys do nothing
		default:
			if !utf8.FullRune(p) {
				e.partial = append([]byte(nil), p...)
				return echo, events
			}
			r, size := utf8.DecodeRune(p)
			e.line = append(e.line, r)
			echo = append(echo, p[:size]...)
			p = p[size:]
			continue
		}
		p = p[1:]
	}
	return echo, events
}

// Reset discards the line being edited.
func (e *Editor) Reset() {
	e.line = nil
	e.partial = nil
}

// skipEscape drops the escape sequence at the start of p (arrow keys,
// function keys, Alt+key), since there is no cursor movement. A sequence
// arrives in a single read, so an Esc at the end of p is a lone Esc.
func skipEscape(p []byte) []byte {
	if len(p) < 2 {
		return nil
	}
	if p[1] != '[' && p[1] != 'O' {
		return p[2:]
	}
	for i := 2; i < len(p); i++ {
		if p[i] >= 0x40 && p[i] <= 0x7e {
			return p[i+1:]
		}
	}
	return nil
}
