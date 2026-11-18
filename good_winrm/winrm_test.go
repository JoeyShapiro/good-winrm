package good_winrm

import (
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// Runs against the test VM (just vm), and is skipped when it isn't up.
// REM_TEST_WINRM_HOST and REM_TEST_WINRM_PASSWORD point it somewhere else.
// testConn is a connection with one goroutine receiving from it for its
// whole life. A reader per waitFor would leave each one blocked in Receive
// after it returns, to take and drop the next chunk.
type testConn struct {
	*WinrmConnection
	chunks chan chunk
}

type chunk struct {
	p   []byte
	err error
}

func testConnection(t *testing.T) *testConn {
	t.Helper()
	return testConnectionRunning(t, "powershell.exe")
}

func testConnectionRunning(t *testing.T, command string) *testConn {
	t.Helper()
	host := envOr("REM_TEST_WINRM_HOST", "localhost")
	password := envOr("REM_TEST_WINRM_PASSWORD", "")

	c, err := NewWinrm(host, 5985, "Administrator", password, command)
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		t.Skipf("no WinRM on %s: %v", host, err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	tc := &testConn{WinrmConnection: c, chunks: make(chan chunk, 64)}
	go func() {
		for {
			p, err := c.Receive()
			tc.chunks <- chunk{p, err}
			if err != nil {
				return
			}
		}
	}()
	return tc
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// waitFor receives until the output contains want, failing after timeout.
func waitFor(t *testing.T, c *testConn, want string, timeout time.Duration) string {
	t.Helper()
	var out strings.Builder
	deadline := time.After(timeout)
	for {
		select {
		case ch := <-c.chunks:
			if ch.err != nil {
				t.Fatalf("receive: %v (output so far %q)", ch.err, out.String())
			}
			out.Write(ch.p)
			if strings.Contains(out.String(), want) {
				return out.String()
			}
		case <-deadline:
			t.Fatalf("no %q after %v (output so far %q)", want, timeout, out.String())
		}
	}
}

// Tells apart the two ways input sent while a poll is waiting on the server
// can stall: the input request itself being held until the poll returns
// (something between here and the server allows one request at a time), or
// the input being accepted but its output only handed over on a later poll
// (the server doesn't answer a waiting poll when output appears). Needs the
// default 60s operation timeout to tell; a short one hides both.
func TestWinrmInputWhilePolling(t *testing.T) {
	c := testConnection(t)
	waitFor(t, c, "PS ", 30*time.Second)

	// Long enough for the next poll to be waiting on the server
	time.Sleep(2 * time.Second)

	start := time.Now()
	if err := c.sendInput([]byte("echo ('he' + 'llo')\n"), false); err != nil {
		t.Fatal(err)
	}
	accepted := time.Since(start)
	t.Logf("input accepted after %v", accepted)
	waitFor(t, c, "hello", 90*time.Second)
	output := time.Since(start)
	t.Logf("output after %v", output)

	switch {
	case accepted > 5*time.Second:
		t.Error("the input request waited for the poll: requests to the server are being serialized")
	case output > 10*time.Second:
		t.Error("the input was accepted, but its output waited for the poll to time out: the server isn't answering a waiting poll when output appears")
	}
}

// Tries sending a line at different times, with different line endings and
// to different shells, to find which ones a server runs. Each case reports
// on its own, so the pattern of passes and failures is the answer.
func TestWinrmInputMatrix(t *testing.T) {
	cases := []struct {
		name    string
		command string
		prompt  string        // waited for before sending; empty sends at once
		idle    time.Duration // after the prompt, before sending
		line    string
	}{
		{"powershell/before prompt/LF", "powershell.exe", "", 0, "6*7\n"},
		{"powershell/at prompt/LF", "powershell.exe", "PS ", 0, "6*7\n"},
		{"powershell/idle 3s/LF", "powershell.exe", "PS ", 3 * time.Second, "6*7\n"},
		{"powershell/idle 3s/CRLF", "powershell.exe", "PS ", 3 * time.Second, "6*7\r\n"},
		{"cmd/before prompt/CRLF", "cmd.exe", "", 0, "set /a 6*7\r\n"},
		{"cmd/idle 3s/LF", "cmd.exe", ">", 3 * time.Second, "set /a 6*7\n"},
		{"cmd/idle 3s/CRLF", "cmd.exe", ">", 3 * time.Second, "set /a 6*7\r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := testConnectionRunning(t, tc.command)
			if tc.prompt != "" {
				waitFor(t, c, tc.prompt, 60*time.Second)
				time.Sleep(tc.idle)
			}
			if err := c.sendInput([]byte(tc.line), false); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			waitFor(t, c, "42", 20*time.Second)
			t.Logf("output after %v", time.Since(start))
		})
	}
}
