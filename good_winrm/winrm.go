package good_winrm

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/masterzen/winrm"
	"github.com/masterzen/winrm/soap"

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
//
// winrm.Command isn't used, since it sends input and polls for output over
// the client's one connection (NTLM allows only one per client). The server
// holds each poll until there's output or the operation timeout (60s), so a
// line typed at an idle prompt would wait that long to be sent. Instead,
// output is polled for on one connection and everything else is sent on
// another.
type WinrmConnection struct {
	client  *winrm.Client // also used for file transfers, on a third connection
	url     string
	recv    winrm.Transporter // only polls for output
	send    winrm.Transporter // input, signals and the shell itself
	shellID string
	cmdID   string

	conns  conns
	editor lineedit.Editor
	echo   echoFilter
	output *connection.Output
}

// conns keeps every TCP connection the transports open, so Close can close
// them; the transports keep them open forever otherwise.
type conns struct {
	mu   sync.Mutex
	open []net.Conn
}

func (c *conns) dial(network, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).Dial(network, addr)
	if err == nil {
		c.mu.Lock()
		c.open = append(c.open, conn)
		c.mu.Unlock()
	}
	return conn, err
}

func (c *conns) closeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, conn := range c.open {
		conn.Close()
	}
	c.open = nil
}

func NewWinrm(host string, port int, username, password, command string) (*WinrmConnection, error) {
	c := &WinrmConnection{
		url: fmt.Sprintf("http://%s/wsman", net.JoinHostPort(host, strconv.Itoa(port))),
	}
	endpoint := winrm.NewEndpoint(host, port, false, false, nil, nil, nil, 0)
	params := *winrm.DefaultParameters
	params.TransportDecorator = func() winrm.Transporter {
		return winrm.NewClientNTLMWithDial(c.conns.dial)
	}

	var err error
	if c.client, err = winrm.NewClientWithParameters(endpoint, username, password, &params); err != nil {
		return nil, err
	}
	c.recv, c.send = params.TransportDecorator(), params.TransportDecorator()
	for _, t := range []winrm.Transporter{c.recv, c.send} {
		if err := t.Transport(endpoint); err != nil {
			return nil, err
		}
	}

	response, err := c.post(c.send, winrm.NewOpenShellRequest(c.url, &c.client.Parameters))
	if err == nil {
		c.shellID, err = winrm.ParseOpenShellResponse(response)
	}
	if err != nil {
		c.conns.closeAll()
		return nil, err
	}

	response, err = c.post(c.send, winrm.NewExecuteCommandRequest(c.url, c.shellID, command, nil, &c.client.Parameters))
	if err == nil {
		c.cmdID, err = winrm.ParseExecuteCommandResponse(response)
	}
	if err != nil {
		c.deleteShell()
		c.conns.closeAll()
		return nil, err
	}

	stdout, stdoutW := io.Pipe()
	stderr, stderrW := io.Pipe()
	go c.poll(stdoutW, stderrW)

	// The remote has no terminal, so nothing turns its bare "\n" into "\r\n"
	var stdoutCRLF, stderrCRLF lineedit.CRLF
	c.output = connection.NewOutput(
		connection.Stream{Reader: stdout, Transform: func(p []byte) []byte {
			return stdoutCRLF.Convert(c.echo.filter(p))
		}},
		connection.Stream{Reader: stderr, Transform: stderrCRLF.Convert},
	)

	return c, nil
}

func (c *WinrmConnection) post(t winrm.Transporter, request *soap.SoapMessage) (string, error) {
	defer request.Free()
	return t.Post(c.client, request)
}

// poll copies the command's output into the pipes until it exits.
func (c *WinrmConnection) poll(stdout, stderr *io.PipeWriter) {
	for {
		response, err := c.post(c.recv, winrm.NewGetOutputRequest(c.url, c.shellID, c.cmdID, "stdout stderr", &c.client.Parameters))
		if err != nil {
			var urlErr *url.Error
			if strings.Contains(err.Error(), "OperationTimeout") || errors.As(err, &urlErr) && urlErr.Timeout() {
				// No output before the server's timeout
				continue
			}
			stdout.CloseWithError(err)
			stderr.CloseWithError(err)
			return
		}

		var outBuf, errBuf bytes.Buffer
		done, _, err := winrm.ParseSlurpOutputErrResponse(response, &outBuf, &errBuf)
		if outBuf.Len() > 0 {
			stdout.Write(outBuf.Bytes())
		}
		if errBuf.Len() > 0 {
			stderr.Write(errBuf.Bytes())
		}
		if err != nil || done {
			// A nil error closes them with io.EOF
			stdout.CloseWithError(err)
			stderr.CloseWithError(err)
			return
		}
	}
}

// sendInput sends data to the command's stdin, then closes it if eof.
func (c *WinrmConnection) sendInput(data []byte, eof bool) error {
	// Leave room in each envelope for the rest of the message
	max := c.client.EnvelopeSize - 1000
	for {
		n := min(len(data), max)
		last := n == len(data)
		if _, err := c.post(c.send, winrm.NewSendInputRequest(c.url, c.shellID, c.cmdID, data[:n], eof && last, &c.client.Parameters)); err != nil {
			return err
		}
		if last {
			return nil
		}
		data = data[n:]
	}
}

func (c *WinrmConnection) Send(p []byte) error {
	echo, events := c.editor.Feed(p)
	c.output.Emit(echo)

	for _, ev := range events {
		switch ev.Kind {
		case lineedit.Line:
			// Older servers (2008) never run a line ending in a bare "\n"
			c.echo.expect(ev.Line + "\n")
			if err := c.sendInput([]byte(ev.Line+"\r\n"), false); err != nil {
				return err
			}
		case lineedit.EOF:
			// The remote exits once its stdin closes, which ends Receive
			return c.sendInput(nil, true)
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
	// Terminating the command also ends the outstanding poll
	_, err := c.post(c.send, winrm.NewSignalRequest(c.url, c.shellID, c.cmdID, &c.client.Parameters))
	err = errors.Join(err, c.deleteShell())
	c.conns.closeAll()
	return err
}

func (c *WinrmConnection) deleteShell() error {
	_, err := c.post(c.send, winrm.NewDeleteShellRequest(c.url, c.shellID, &c.client.Parameters))
	return err
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
