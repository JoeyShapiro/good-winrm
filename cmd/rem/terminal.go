package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"good-winrm/connection"
	"good-winrm/lineedit"
)

// Ctrl+` toggles the meta terminal. Terminals send it as NUL.
const keyMetaToggle = 0x00

// Ctrl+C stops reconnecting.
const keyCtrlC = 0x03

// terminal routes raw input either to the connection or, while toggled on,
// to the meta terminal. All of its state belongs to the main loop.
type terminal struct {
	conn      connection.Connection
	connected bool
	dial      func() (connection.Connection, error)
	name      string // shown in system messages

	screen *screen
	meta   bool
	editor lineedit.Editor

	// Non-nil while reconnecting in the background
	stopReconnecting context.CancelFunc

	received    chan received
	reconnected chan connection.Connection
	resized     chan size
	size        size
}

// received is how a connection's output ended.
type received struct {
	conn connection.Connection
	err  error
}

type size struct {
	width, height int
}

// receive shows conn's output until it ends, then reports how.
func (t *terminal) receive(conn connection.Connection) {
	go func() {
		for {
			p, err := conn.Receive()
			if err != nil {
				t.received <- received{conn, err}
				return
			}
			t.screen.raw(p)
		}
	}()
}

func (t *terminal) resize() {
	if r, ok := t.conn.(connection.Resizer); ok && t.connected && t.size.width > 0 {
		r.Resize(t.size.width, t.size.height)
	}
}
func (t *terminal) handleInput(p []byte) error {
	if t.stopReconnecting != nil {
		// Reconnecting acts like a running meta command: only Ctrl+C does anything
		if bytes.IndexByte(p, keyCtrlC) >= 0 {
			t.stopReconnecting()
			t.stopReconnecting = nil
			t.screen.log("stopped reconnecting (reconnect to try again, quit to exit)")
			fmt.Fprint(t.screen, metaPrompt)
		}
		return nil
	}

	for {
		i := bytes.IndexByte(p, keyMetaToggle)
		if i < 0 {
			return t.send(p)
		}
		if err := t.send(p[:i]); err != nil {
			return err
		}
		t.toggleMeta()
		p = p[i+1:]
	}
}

func (t *terminal) toggleMeta() {
	if !t.connected {
		fmt.Fprint(t.screen, "\n")
		t.notConnected()
		return
	}
	t.meta = !t.meta
	t.editor.Reset()
	fmt.Fprint(t.screen, "\n")
	if t.meta {
		fmt.Fprint(t.screen, metaPrompt)
	}
}

// notConnected explains why the meta terminal can't be left.
func (t *terminal) notConnected() {
	fmt.Fprintf(t.screen, "error: not connected (reconnect to try again, quit to exit)\n")
	fmt.Fprint(t.screen, metaPrompt)
}

func (t *terminal) send(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	if !t.meta {
		return t.conn.Send(p)
	}

	echo, events := t.editor.Feed(p)
	t.screen.Write(echo)
	for _, ev := range events {
		switch ev.Kind {
		case lineedit.Line:
			if err := t.evalMetaCommand(ev.Line); errors.Is(err, errQuit) {
				return err
			} else if err != nil {
				fmt.Fprintf(t.screen, "error: %v\n", err)
			}
		case lineedit.EOF:
			if !t.connected {
				fmt.Fprint(t.screen, "\n")
				t.notConnected()
				continue
			}
			t.meta = false
			fmt.Fprint(t.screen, "\n")
		}
		if t.meta && t.stopReconnecting == nil {
			fmt.Fprint(t.screen, metaPrompt)
		}
	}
	return nil
}
