package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"
	"sync"

	"github.com/google/shlex"
	"github.com/masterzen/winrm"
	"golang.org/x/term"
)

/*
# Enable Basic auth
Set-Item WSMan:\localhost\Service\Auth\Basic $true

# Allow unencrypted traffic (HTTP)
Set-Item WSMan:\localhost\Service\AllowUnencrypted $true

# Restart WinRM
Restart-Service WinRM
*/

// Keys we care about while stdin is in raw mode.
const (
	keyMetaToggle = 0x00 // Ctrl+` (terminals send NUL, same as Ctrl+@ / Ctrl+Space)
	keyCtrlC      = 0x03
	keyCtrlD      = 0x04
	keyCtrlH      = 0x08
	keyCtrlU      = 0x15
	keyEsc        = 0x1b
	keyBackspace  = 0x7f
)

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

	endpoint := winrm.NewEndpoint(host, *port, false, false, nil, nil, nil, 0)
	params := winrm.DefaultParameters

	params.TransportDecorator = func() winrm.Transporter {
		return &winrm.ClientNTLM{}
	}

	client, err := winrm.NewClientWithParameters(endpoint, username, string(password), params)
	if err != nil {
		return err
	}

	shell, err := client.CreateShell()
	if err != nil {
		return err
	}
	defer shell.Close()

	ps, err := shell.ExecuteWithContext(context.Background(), *command)
	if err != nil {
		return err
	}
	defer ps.Close()

	oldState, err := term.MakeRaw(stdinFd)
	if err != nil {
		return err
	}
	defer term.Restore(stdinFd, oldState)

	s := &session{
		client: client,
		ps:     ps,
		out:    &crlfWriter{w: os.Stdout},
		echo:   &echoFilter{},
	}

	remoteDone := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ps.Stdout.Read(buf)
			if n > 0 {
				s.out.Write(s.echo.filter(buf[:n]))
			}
			if err != nil {
				break
			}
		}
		close(remoteDone)
	}()
	go io.Copy(&crlfWriter{w: os.Stderr}, ps.Stderr)

	keys := make(chan rune)
	go readKeys(bufio.NewReader(os.Stdin), keys)

	for {
		select {
		case <-remoteDone:
			return nil
		case r, ok := <-keys:
			if !ok {
				return nil
			}
			if err := s.handleKey(r); err != nil {
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

// readKeys forwards keypresses from raw stdin, dropping escape sequences
// (arrow keys, function keys, Alt+key) since there is no cursor movement.
func readKeys(in *bufio.Reader, keys chan<- rune) {
	defer close(keys)
	for {
		r, _, err := in.ReadRune()
		if err != nil {
			return
		}
		if r == keyEsc {
			// A lone Esc arrives by itself; a sequence arrives in one read.
			if in.Buffered() == 0 {
				continue
			}
			next, _, _ := in.ReadRune()
			if next == '[' || next == 'O' {
				for in.Buffered() > 0 {
					b, _ := in.ReadByte()
					if b >= 0x40 && b <= 0x7e {
						break
					}
				}
			}
			continue
		}
		keys <- r
	}
}

// session does local line editing in raw mode and routes completed lines
// either to the remote process or to the meta terminal.
type session struct {
	client *winrm.Client
	ps     *winrm.Command
	out    io.Writer
	echo   *echoFilter
	meta   bool
	line   []rune
}

func (s *session) handleKey(r rune) error {
	switch r {
	case keyMetaToggle:
		s.line = nil
		s.meta = !s.meta
		fmt.Fprint(s.out, "\n")
		if s.meta {
			fmt.Fprint(s.out, metaPrompt)
		}
	case keyCtrlC:
		s.line = nil
		fmt.Fprint(s.out, "^C\n")
		if s.meta {
			fmt.Fprint(s.out, metaPrompt)
		}
	case keyCtrlD:
		if len(s.line) > 0 {
			return nil
		}
		if s.meta {
			s.meta = false
			fmt.Fprint(s.out, "\n")
			return nil
		}
		// EOF on the remote stdin; the remote exits and stdout closes.
		return s.ps.Stdin.Close()
	case keyBackspace, keyCtrlH:
		if len(s.line) > 0 {
			s.line = s.line[:len(s.line)-1]
			fmt.Fprint(s.out, "\b \b")
		}
	case keyCtrlU:
		fmt.Fprint(s.out, strings.Repeat("\b \b", len(s.line)))
		s.line = nil
	case '\r', '\n':
		input := string(s.line)
		s.line = nil
		fmt.Fprint(s.out, "\n")
		if s.meta {
			if err := s.evalMetaCommand(input); err != nil {
				fmt.Fprintf(s.out, "error: %v\n", err)
			}
			if s.meta {
				fmt.Fprint(s.out, metaPrompt)
			}
			return nil
		}
		s.echo.expect(input + "\n")
		_, err := s.ps.Stdin.Write([]byte(input + "\n"))
		return err
	default:
		if r < 0x20 && r != '\t' {
			return nil
		}
		s.line = append(s.line, r)
		fmt.Fprint(s.out, string(r))
	}
	return nil
}

func (s *session) evalMetaCommand(input string) error {
	args, err := shlex.Split(input)
	if err != nil {
		return err
	}

	if len(args) == 0 {
		return nil
	}

	switch args[0] {
	case "exit":
		s.meta = false
	case "upload":
		if len(args) != 3 {
			return errors.New("usage: upload <local_path> <remote_path>")
		}
		return uploadFile(s.client, s.out, args[1], args[2])
	case "download":
		if len(args) != 3 {
			return errors.New("usage: download <remote_path> <local_path>")
		}
		return downloadFile(s.client, s.out, args[1], args[2])
	default:
		return errors.New("unknown meta command: " + args[0])
	}
	return nil
}

// echoFilter drops the remote shell's echo of a line we sent, since it was
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
	f.pending = []byte(line)
	f.held = nil
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
			if len(f.pending) == 0 {
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

// psQuote quotes s as a PowerShell single-quoted string literal.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func runPS(client *winrm.Client, script string) (string, error) {
	stdout, stderr, _, err := client.RunPSWithContext(context.Background(), script)
	if err != nil {
		return "", err
	} else if stderr != "" {
		return "", errors.New(stderr)
	}
	return stdout, nil
}

func uploadFile(client *winrm.Client, out io.Writer, localPath, remotePath string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}

	// Create or truncate, so empty files still get created
	_, err = runPS(client, fmt.Sprintf("[System.IO.File]::WriteAllBytes(%s, [byte[]]@())", psQuote(remotePath)))
	if err != nil {
		return err
	}

	// Chunk size (stay well under 8KB command limit)
	chunkSize := 5000

	for i := 0; i < len(data); i += chunkSize {
		end := min(i+chunkSize, len(data))
		encoded := base64.StdEncoding.EncodeToString(data[i:end])

		_, err := runPS(client, fmt.Sprintf(`
            $data = [System.Convert]::FromBase64String('%s')
            Add-Content -Path %s -Value $data -Encoding Byte
        `, encoded, psQuote(remotePath)))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %d%% (%d/%d) bytes uploaded\n", remotePath, end*100/len(data), end, len(data))
	}

	return nil
}

func downloadFile(client *winrm.Client, out io.Writer, remotePath, localPath string) error {
	// Read and encode on remote side
	stdout, err := runPS(client, fmt.Sprintf(`
        $data = [System.IO.File]::ReadAllBytes(%s)
        [System.Convert]::ToBase64String($data)
    `, psQuote(remotePath)))
	if err != nil {
		return err
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stdout))
	if err != nil {
		return err
	}

	if err := os.WriteFile(localPath, decoded, 0600); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %d bytes downloaded\n", localPath, len(decoded))
	return nil
}
