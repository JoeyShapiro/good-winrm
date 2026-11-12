package good_winrm

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/masterzen/winrm"

	"good-winrm/connection"
	"good-winrm/lineedit"
)

/*
# Enable Basic auth
Set-Item WSMan:\localhost\Service\Auth\Basic $true

# Allow unencrypted traffic (HTTP)
Set-Item WSMan:\localhost\Service\AllowUnencrypted $true

# Restart WinRM
Restart-Service WinRM
*/

var (
	_ connection.Connection = (*WinrmConnection)(nil)
	_ connection.Uploader   = (*WinrmConnection)(nil)
	_ connection.Downloader = (*WinrmConnection)(nil)
)

// WinrmConnection runs a command over WinRM. WinRM has no terminal on the
// remote side, so input is line-edited and echoed locally, and only whole
// lines are sent.
type WinrmConnection struct {
	client *winrm.Client
	shell  *winrm.Shell
	cmd    *winrm.Command

	editor lineedit.Editor
	echo   echoFilter

	output chan []byte
	done   chan struct{} // closed once remote stdout and stderr have ended
}

func NewWinrm(host string, port int, username, password, command string) (*WinrmConnection, error) {
	endpoint := winrm.NewEndpoint(host, port, false, false, nil, nil, nil, 0)
	params := winrm.DefaultParameters

	params.TransportDecorator = func() winrm.Transporter {
		return &winrm.ClientNTLM{}
	}

	client, err := winrm.NewClientWithParameters(endpoint, username, password, params)
	if err != nil {
		return nil, err
	}

	shell, err := client.CreateShell()
	if err != nil {
		return nil, err
	}

	cmd, err := shell.ExecuteWithContext(context.Background(), command)
	if err != nil {
		shell.Close()
		return nil, err
	}

	c := &WinrmConnection{
		client: client,
		shell:  shell,
		cmd:    cmd,
		output: make(chan []byte, 16),
		done:   make(chan struct{}),
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		c.pump(cmd.Stdout, c.echo.filter)
	}()
	go func() {
		defer wg.Done()
		c.pump(cmd.Stderr, func(p []byte) []byte { return append([]byte(nil), p...) })
	}()
	go func() {
		wg.Wait()
		close(c.done)
	}()

	return c, nil
}

// pump forwards a remote stream to Receive.
func (c *WinrmConnection) pump(r io.Reader, transform func([]byte) []byte) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if out := transform(buf[:n]); len(out) > 0 {
				c.output <- out
			}
		}
		if err != nil {
			return
		}
	}
}

// emit queues locally generated output, such as the echo of typed keys.
func (c *WinrmConnection) emit(p []byte) {
	if len(p) == 0 {
		return
	}
	select {
	case c.output <- p:
	case <-c.done:
	}
}

func (c *WinrmConnection) Send(p []byte) error {
	echo, events := c.editor.Feed(p)
	c.emit(echo)

	for _, ev := range events {
		switch ev.Kind {
		case lineedit.Line:
			line := ev.Line + "\n"
			c.echo.expect(line)
			if _, err := c.cmd.Stdin.Write([]byte(line)); err != nil {
				return err
			}
		case lineedit.EOF:
			// The remote exits once its stdin closes, which ends Receive
			return c.cmd.Stdin.Close()
		case lineedit.Interrupt:
			// WinRM can't deliver Ctrl+C; the editor has already discarded the line
		}
	}
	return nil
}

func (c *WinrmConnection) Receive() ([]byte, error) {
	select {
	case p := <-c.output:
		return p, nil
	case <-c.done:
		// Drain anything queued before the streams ended
		select {
		case p := <-c.output:
			return p, nil
		default:
			return nil, io.EOF
		}
	}
}

func (c *WinrmConnection) Close() error {
	return errors.Join(c.cmd.Close(), c.shell.Close())
}

// echoFilter drops the remote shell's echo of lines we sent, since they were
// already echoed locally while being typed. If the output diverges from the
// expected echo, everything held back is passed through untouched.
type echoFilter struct {
	mu      sync.Mutex
	pending []byte
	held    []byte
}

func (f *echoFilter) expect(line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, line...)
}

func (f *echoFilter) filter(p []byte) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []byte
	for i, b := range p {
		if len(f.pending) == 0 {
			return append(out, p[i:]...)
		}
		switch {
		case b == f.pending[0]:
			f.held = append(f.held, b)
			f.pending = f.pending[1:]
			// Each completed line's echo is fully matched; stop holding it
			if b == '\n' {
				f.held = nil
			}
		case b == '\r' && f.pending[0] == '\n':
			f.held = append(f.held, b)
		default:
			out = append(out, f.held...)
			out = append(out, b)
			f.held = nil
			f.pending = nil
		}
	}
	return out
}
