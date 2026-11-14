package good_winrm

import (
	"context"
	"errors"
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
	output *connection.Output
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
	}

	// The remote has no terminal, so nothing turns its bare "\n" into "\r\n"
	var stdoutCRLF, stderrCRLF lineedit.CRLF
	c.output = connection.NewOutput(
		connection.Stream{Reader: cmd.Stdout, Transform: func(p []byte) []byte {
			return stdoutCRLF.Convert(c.echo.filter(p))
		}},
		connection.Stream{Reader: cmd.Stderr, Transform: stderrCRLF.Convert},
	)

	return c, nil
}

func (c *WinrmConnection) Send(p []byte) error {
	echo, events := c.editor.Feed(p)
	c.output.Emit(echo)

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
	return c.output.Receive()
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
