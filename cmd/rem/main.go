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
	"good-winrm/good_winrm"
	"good-winrm/lineedit"
)

// Ctrl+` toggles the meta terminal. Terminals send it as NUL.
const keyMetaToggle = 0x00

const metaPrompt = "\033[32m(good-winrm)\033[0m "

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("good-winrm", flag.ExitOnError)
	port := fs.Int("port", 5985, "WinRM port")
	command := fs.String("command", "powershell.exe", "Command to execute")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: good-winrm [user@]host [--port 5985] [--command powershell.exe]\n")
		fs.PrintDefaults()
	}

	// flag stops at the first positional argument, so parse again after it
	// to allow flags on either side of the destination.
	fs.Parse(os.Args[1:])
	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(2)
	}
	destination := fs.Arg(0)
	fs.Parse(fs.Args()[1:])
	if fs.NArg() > 0 {
		fs.Usage()
		os.Exit(2)
	}

	username, host, err := parseDestination(destination)
	if err != nil {
		return err
	}

	stdinFd := int(os.Stdin.Fd())
	if !term.IsTerminal(stdinFd) {
		return errors.New("stdin must be a terminal")
	}

	fmt.Fprintf(os.Stderr, "%s@%s's password: ", username, host)
	password, err := term.ReadPassword(stdinFd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}

	conn, err := good_winrm.NewWinrm(host, *port, username, string(password), *command)
	if err != nil {
		return err
	}
	defer conn.Close()

	oldState, err := term.MakeRaw(stdinFd)
	if err != nil {
		return err
	}
	defer term.Restore(stdinFd, oldState)

	t := &terminal{
		conn: conn,
		out:  &crlfWriter{w: os.Stdout},
	}

	remoteDone := make(chan error, 1)
	go func() {
		for {
			p, err := conn.Receive()
			if err != nil {
				remoteDone <- err
				return
			}
			t.out.Write(p)
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
			if err := t.handleInput(p); err != nil {
				return err
			}
		}
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
			if err := t.evalMetaCommand(ev.Line); err != nil {
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
	last byte
}

func (c *crlfWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	buf := make([]byte, 0, len(p))
	for _, b := range p {
		if b == '\n' && c.last != '\r' {
			buf = append(buf, '\r')
		}
		buf = append(buf, b)
		c.last = b
	}
	if _, err := c.w.Write(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}
