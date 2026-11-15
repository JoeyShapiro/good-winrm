package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"
	"sync"

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

	var conn connection.Connection
	var err error
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
		conn, err = good_winrm.NewWinrm(host, *port, username, password, *command)
		if err != nil {
			return err
		}
	case "ssh":
		username, host, err := parseDestination(destination)
		if err != nil {
			return err
		}
		if *port == 0 {
			*port = 22
		}
		conn, err = good_ssh.NewSsh(host, *port, username, *command, prompt)
		if err != nil {
			return err
		}
	case "serial":
		conn, err = good_serial.NewSerial(destination, *baud)
		if err != nil {
			return err
		}
	default:
		fs.Usage()
		os.Exit(2)
	}
	defer conn.Close()

	oldState, err := term.MakeRaw(stdinFd)
	if err != nil {
		return err
	}
	defer term.Restore(stdinFd, oldState)

	// The connection's output goes to the terminal untouched, while the meta
	// terminal's own output needs "\r\n" in raw mode
	t := &terminal{
		conn: conn,
		out:  &crlfWriter{w: os.Stdout},
	}

	if r, ok := conn.(connection.Resizer); ok {
		go watchResize(int(os.Stdout.Fd()), func(width, height int) {
			r.Resize(width, height)
		})
	}

	remoteDone := make(chan error, 1)
	go func() {
		for {
			p, err := conn.Receive()
			if err != nil {
				remoteDone <- err
				return
			}
			os.Stdout.Write(p)
		}
	}()

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
		case err := <-remoteDone:
			if err == io.EOF {
				return nil
			}
			return err
		case p, ok := <-input:
			if !ok {
				return nil
			}
			if err := t.handleInput(p); errors.Is(err, errQuit) {
				return nil
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

// terminal routes raw input either to the connection or, while toggled on,
// to the meta terminal.
type terminal struct {
	conn   connection.Connection
	out    io.Writer
	meta   bool
	editor lineedit.Editor
}

func (t *terminal) handleInput(p []byte) error {
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
	t.meta = !t.meta
	t.editor.Reset()
	fmt.Fprint(t.out, "\n")
	if t.meta {
		fmt.Fprint(t.out, metaPrompt)
	}
}

func (t *terminal) send(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	if !t.meta {
		return t.conn.Send(p)
	}

	echo, events := t.editor.Feed(p)
	t.out.Write(echo)
	for _, ev := range events {
		switch ev.Kind {
		case lineedit.Line:
			if err := t.evalMetaCommand(ev.Line); errors.Is(err, errQuit) {
				return err
			} else if err != nil {
				fmt.Fprintf(t.out, "error: %v\n", err)
			}
		case lineedit.EOF:
			t.meta = false
			fmt.Fprint(t.out, "\n")
		}
		if t.meta {
			fmt.Fprint(t.out, metaPrompt)
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
		t.meta = false
	case "quit", "bye":
		return errQuit
	case "upload":
		if len(args) != 3 {
			return errors.New("usage: upload <local_path> <remote_path>")
		}
		u, ok := t.conn.(connection.Uploader)
		if !ok {
			return errors.New("upload is not supported by this connection")
		}
		return u.Upload(args[1], args[2], t.out)
	case "download":
		if len(args) != 3 {
			return errors.New("usage: download <remote_path> <local_path>")
		}
		d, ok := t.conn.(connection.Downloader)
		if !ok {
			return errors.New("download is not supported by this connection")
		}
		return d.Download(args[1], args[2], t.out)
	default:
		return errors.New("unknown meta command: " + args[0])
	}
	return nil
}

// crlfWriter turns bare "\n" into "\r\n", which raw mode no longer does for us.
type crlfWriter struct {
	mu   sync.Mutex
	w    io.Writer
	crlf lineedit.CRLF
}

func (c *crlfWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, err := c.w.Write(c.crlf.Convert(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}
