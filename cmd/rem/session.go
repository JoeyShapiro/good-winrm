package main

import (
	"errors"
	"io"
	"os"

	"golang.org/x/term"

	"good-winrm/connection"
)

// runSession connects with dial and runs the interactive session, with name
// shown in system messages.
func runSession(dial func() (connection.Connection, error), name string) error {
	stdinFd := int(os.Stdin.Fd())

	conn, err := dial()
	if err != nil {
		return err
	}

	oldState, err := term.MakeRaw(stdinFd)
	if err != nil {
		conn.Close()
		return err
	}
	defer term.Restore(stdinFd, oldState)

	t := &terminal{
		conn:        conn,
		connected:   true,
		dial:        dial,
		name:        name,
		screen:      &screen{w: os.Stdout, last: '\n'},
		received:    make(chan received),
		reconnected: make(chan connection.Connection),
		resized:     make(chan size, 1),
	}
	defer func() { t.conn.Close() }()
	t.receive(conn)

	go watchResize(int(os.Stdout.Fd()), func(width, height int) {
		t.resized <- size{width, height}
	})

	input := make(chan []byte)
	go func() {
		defer close(input)
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				input <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case r := <-t.received:
			if r.conn != t.conn {
				// From a connection that was already replaced
				continue
			}
			if errors.Is(r.err, connection.ErrDisconnected) {
				t.disconnected(r.err)
			} else if r.err == io.EOF {
				return nil
			} else {
				return r.err
			}
		case conn := <-t.reconnected:
			t.reconnect(conn)
		case sz := <-t.resized:
			t.size = sz
			t.resize()
		case p, ok := <-input:
			if !ok {
				return nil
			}
			err := t.handleInput(p)
			if errors.Is(err, errQuit) {
				return nil
			} else if errors.Is(err, connection.ErrDisconnected) {
				t.disconnected(err)
			} else if err != nil {
				return err
			}
		}
	}
}
