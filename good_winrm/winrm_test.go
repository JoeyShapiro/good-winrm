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
func testConnection(t *testing.T) *WinrmConnection {
	t.Helper()
	host := envOr("REM_TEST_WINRM_HOST", "localhost")
	password := envOr("REM_TEST_WINRM_PASSWORD", "")

	c, err := NewWinrm(host, 5985, "Administrator", password, "powershell.exe")
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		t.Skipf("no WinRM on %s: %v", host, err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// waitFor receives until the output contains want, failing after timeout.
func waitFor(t *testing.T, c *WinrmConnection, want string, timeout time.Duration) string {
	t.Helper()
	type chunk struct {
		p   []byte
		err error
	}
	chunks := make(chan chunk)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			p, err := c.Receive()
			select {
			case chunks <- chunk{p, err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	var out strings.Builder
	deadline := time.After(timeout)
	for {
		select {
		case ch := <-chunks:
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
