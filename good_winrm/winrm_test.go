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

// Goes through Send, as typing does, rather than sendInput.
func TestWinrmTypedLineRuns(t *testing.T) {
	c := testConnection(t)
	waitFor(t, c, "PS ", 60*time.Second)
	time.Sleep(3 * time.Second)
	if err := c.Send([]byte("6*7\r")); err != nil {
		t.Fatal(err)
	}
	out := waitFor(t, c, "42", 20*time.Second)
	if strings.Contains(out, "6*76*7") {
		t.Errorf("the remote's echo wasn't filtered out: %q", out)
	}
}
