package main

import (
	"context"
	"time"

	"good-winrm/connection"
)

// How often to retry while reconnecting.
const reconnectInterval = 500 * time.Millisecond

// disconnected pops into the meta terminal and starts reconnecting.
func (t *terminal) disconnected(err error) {
	if !t.connected {
		return
	}
	t.connected = false
	t.conn.Close()
	t.meta = true
	t.editor.Reset()
	t.screen.log("%s %v", t.name, err)
	t.startReconnecting()
}

func (t *terminal) startReconnecting() {
	ctx, cancel := context.WithCancel(context.Background())
	t.stopReconnecting = cancel
	t.screen.log("reconnecting (Ctrl+C to stop)")

	go func() {
		var lastErr string
		for {
			conn, err := t.dial()
			if err == nil {
				select {
				case t.reconnected <- conn:
				case <-ctx.Done():
					conn.Close()
				}
				return
			}
			// Only report new kinds of failure, not every attempt
			if err.Error() != lastErr {
				lastErr = err.Error()
				t.screen.log("%v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(reconnectInterval):
			}
		}
	}()
}

// reconnect switches to conn and goes back to the remote terminal.
func (t *terminal) reconnect(conn connection.Connection) {
	if t.stopReconnecting == nil {
		// Reconnecting was stopped just as this connection was made
		conn.Close()
		return
	}
	t.stopReconnecting()
	t.stopReconnecting = nil

	t.conn = conn
	t.connected = true
	t.meta = false
	t.editor.Reset()
	t.screen.log("reconnected to %s", t.name)
	t.receive(conn)
	t.resize()
}
