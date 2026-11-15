package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/google/shlex"
	"golang.org/x/term"

	"good-winrm/connection"
	"good-winrm/good_serial"
	"good-winrm/good_ssh"
	"good-winrm/good_winrm"
	"good-winrm/lineedit"
)

// Ctrl+` toggles the meta terminal. Terminals send it as NUL.
const keyMetaToggle = 0x00

// Ctrl+C stops reconnecting.
const keyCtrlC = 0x03

// errQuit ends the session from the meta terminal.
var errQuit = errors.New("quit")

const metaPrompt = "\033[32m(rem)\033[0m "

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("rem", flag.ExitOnError)
	port := fs.Int("port", 0, "Port (default 5985 for winrm, 22 for ssh)")
	command := fs.String("command", "", "Command to execute (default powershell.exe for winrm, a login shell for ssh)")
	baud := fs.Int("baud", 115200, "Baud rate for serial")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: rem winrm [user@]host [--port N] [--command CMD]\n")
		fmt.Fprintf(os.Stderr, "       rem ssh [user@]host [--port N] [--command CMD]\n")
		fmt.Fprintf(os.Stderr, "       rem serial device [--baud RATE]\n")
		fs.PrintDefaults()
	}

	// flag stops at the first positional argument, so keep parsing after
	// each one to allow flags anywhere.
	var positional []string
	rest := os.Args[1:]
	for {
		fs.Parse(rest)
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(positional) != 2 {
		fs.Usage()
		os.Exit(2)
	}
	protocol, destination := positional[0], positional[1]

	stdinFd := int(os.Stdin.Fd())
	if !term.IsTerminal(stdinFd) {
		return errors.New("stdin must be a terminal")
	}

	var dial func() (connection.Connection, error)
	switch protocol {
	case "winrm":
		username, host, err := parseDestination(destination)
		if err != nil {
			return err
		}
		if *port == 0 {
			*port = 5985
		}
		if *command == "" {
			*command = "powershell.exe"
		}
		password, err := prompt(fmt.Sprintf("%s@%s's password: ", username, host), false)
		if err != nil {
			return err
		}
		dial = func() (connection.Connection, error) {
			return good_winrm.NewWinrm(host, *port, username, password, *command)
		}
	case "ssh":
		username, host, err := parseDestination(destination)
		if err != nil {
			return err
		}
		if *port == 0 {
			*port = 22
		}
		dial = func() (connection.Connection, error) {
			return good_ssh.NewSsh(host, *port, username, *command, prompt)
		}
	case "serial":
		dial = func() (connection.Connection, error) {
			return good_serial.NewSerial(destination, *baud)
		}
	default:
		fs.Usage()
		os.Exit(2)
	}

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
		name:        destination,
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

// prompt asks a question on the terminal before it is in raw mode, hiding
// the answer unless echo is set.
func prompt(question string, echo bool) (string, error) {
	fmt.Fprint(os.Stderr, question)
	if !echo {
		answer, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(answer), err
	}

	// Read a byte at a time so nothing past the newline is consumed
	var answer []byte
	b := make([]byte, 1)
	for {
		if _, err := os.Stdin.Read(b); err != nil {
			return "", err
		}
		if b[0] == '\n' {
			return strings.TrimRight(string(answer), "\r"), nil
		}
		answer = append(answer, b[0])
	}
}

// parseDestination splits [user@]host, defaulting to the local username like
// ssh does. The last @ is the separator, so user@domain.com@host works.
func parseDestination(dest string) (username, host string, err error) {
	if i := strings.LastIndex(dest, "@"); i >= 0 {
		username, host = dest[:i], dest[i+1:]
	} else {
		u, err := user.Current()
		if err != nil {
			return "", "", err
		}
		// On Windows this is DOMAIN\user
		username, host = u.Username, dest
	}
	if username == "" || host == "" {
		return "", "", fmt.Errorf("invalid destination: %q", dest)
	}
	return username, host, nil
}

// How often to retry while reconnecting.
const reconnectInterval = 500 * time.Millisecond

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

func (t *terminal) evalMetaCommand(input string) error {
	args, err := shlex.Split(input)
	if err != nil {
		return err
	}

	if len(args) == 0 {
		return nil
	}

	switch args[0] {
	case "exit":
		if !t.connected {
			return errors.New("not connected (reconnect to try again, quit to exit)")
		}
		t.meta = false
	case "quit", "bye":
		return errQuit
	case "reconnect":
		if t.connected {
			return errors.New("already connected")
		}
		t.startReconnecting()
	case "upload":
		if len(args) != 3 {
			return errors.New("usage: upload <local_path> <remote_path>")
		}
		if !t.connected {
			return errors.New("upload: not connected")
		}
		u, ok := t.conn.(connection.Uploader)
		if !ok {
			return errors.New("upload is not supported by this connection")
		}
		return u.Upload(args[1], args[2], t.screen)
	case "download":
		if len(args) != 3 {
			return errors.New("usage: download <remote_path> <local_path>")
		}
		if !t.connected {
			return errors.New("download: not connected")
		}
		d, ok := t.conn.(connection.Downloader)
		if !ok {
			return errors.New("download is not supported by this connection")
		}
		return d.Download(args[1], args[2], t.screen)
	default:
		return errors.New("unknown meta command: " + args[0])
	}
	return nil
}

// screen serializes writes to the terminal, and tracks whether the cursor
// is at the start of a line so system messages never start mid-line.
type screen struct {
	mu   sync.Mutex
	w    io.Writer
	crlf lineedit.CRLF
	last byte
}

// raw writes the connection's output untouched.
func (s *screen) raw(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.write(p)
}

// Write is for the meta terminal's own output, which needs "\r\n" in raw
// mode.
func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.write(s.crlf.Convert(p))
	return len(p), nil
}

// log prints a timestamped system message on its own line.
func (s *screen) log(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last != '\n' {
		s.write([]byte("\r\n"))
	}
	msg := fmt.Sprintf(format, args...)
	s.write([]byte(fmt.Sprintf("\033[33m[rem %s] %s\033[0m\r\n", time.Now().Format("15:04:05"), msg)))
}

func (s *screen) write(p []byte) {
	if len(p) == 0 {
		return
	}
	s.w.Write(p)
	s.last = p[len(p)-1]
}
