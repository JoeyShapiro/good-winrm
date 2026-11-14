package good_ssh

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Upload and Download speak the scp protocol to the remote's scp binary
// (scp -t / scp -f), which OpenSSH provides on Linux and Windows alike.

func (c *SshConnection) Upload(localPath, remotePath string, progress io.Writer) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", localPath)
	}

	return c.scp("scp -t "+remoteQuote(remotePath), func(r *bufio.Reader, w io.Writer) error {
		if err := readAck(r); err != nil {
			return err
		}
		fmt.Fprintf(w, "C%04o %d %s\n", info.Mode().Perm(), info.Size(), filepath.Base(localPath))
		if err := readAck(r); err != nil {
			return err
		}
		pw := &progressWriter{w: progress, path: remotePath, total: info.Size(), verb: "uploaded"}
		if _, err := io.Copy(io.MultiWriter(w, pw), f); err != nil {
			return err
		}
		pw.finish()
		w.Write([]byte{0})
		return readAck(r)
	})
}

func (c *SshConnection) Download(remotePath, localPath string, progress io.Writer) error {
	return c.scp("scp -f "+remoteQuote(remotePath), func(r *bufio.Reader, w io.Writer) error {
		w.Write([]byte{0})

		header, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		switch header[0] {
		case 1, 2:
			return scpError(header[1:])
		case 'D':
			return fmt.Errorf("%s is a directory", remotePath)
		case 'C':
		default:
			return fmt.Errorf("scp: unexpected response %q", header)
		}

		// C<mode> <size> <name>
		fields := strings.SplitN(strings.TrimSpace(header[1:]), " ", 3)
		if len(fields) != 3 {
			return fmt.Errorf("scp: malformed header %q", header)
		}
		size, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return fmt.Errorf("scp: malformed header %q", header)
		}
		w.Write([]byte{0})

		f, err := os.OpenFile(localPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer f.Close()

		pw := &progressWriter{w: progress, path: localPath, total: size, verb: "downloaded"}
		if _, err := io.CopyN(io.MultiWriter(f, pw), r, size); err != nil {
			return err
		}
		pw.finish()
		if err := readAck(r); err != nil {
			return err
		}
		w.Write([]byte{0})
		return f.Close()
	})
}

// scp runs command in its own session and hands its stdout and stdin to
// transfer.
func (c *SshConnection) scp(command string, transfer func(r *bufio.Reader, w io.Writer) error) error {
	session, err := c.client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	session.Stderr = &stderr

	if err := session.Start(command); err != nil {
		return err
	}

	err = transfer(bufio.NewReader(stdout), stdin)
	stdin.Close()
	if err != nil {
		// The remote may be blocked writing output nobody will read
		session.Close()
	}
	if waitErr := session.Wait(); err == nil {
		err = waitErr
	}
	if err != nil && stderr.Len() > 0 {
		// e.g. scp missing on the remote
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return err
}

// readAck reads the remote scp's reply: 0 for ok, or 1/2 and a message.
func readAck(r *bufio.Reader) error {
	b, err := r.ReadByte()
	if err != nil {
		return err
	}
	if b == 0 {
		return nil
	}
	msg, _ := r.ReadString('\n')
	return scpError(msg)
}

// scpError wraps a message from the remote scp, which usually already
// starts with "scp: ".
func scpError(msg string) error {
	msg = strings.TrimSpace(msg)
	if !strings.HasPrefix(msg, "scp:") {
		msg = "scp: " + msg
	}
	return errors.New(msg)
}

// remoteQuote quotes a path for the remote shell. The remote OS isn't known,
// so Windows-looking paths get double quotes (cmd.exe has no single quotes)
// and everything else gets POSIX single quotes. A leading ~/ stays unquoted
// so it still expands.
func remoteQuote(path string) string {
	if strings.ContainsRune(path, '\\') || (len(path) >= 2 && path[1] == ':') {
		return `"` + path + `"`
	}
	prefix := ""
	if strings.HasPrefix(path, "~/") {
		prefix, path = "~/", path[2:]
	}
	return prefix + "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// progressWriter reports transfer progress on one line, as a percentage.
type progressWriter struct {
	w       io.Writer
	path    string
	verb    string
	total   int64
	done    int64
	percent int64
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if percent := p.done * 100 / max(p.total, 1); percent != p.percent {
		p.percent = percent
		p.print()
	}
	return len(b), nil
}

func (p *progressWriter) print() {
	fmt.Fprintf(p.w, "\r%s: %d%% (%d/%d) bytes %s", p.path, p.percent, p.done, p.total, p.verb)
}

func (p *progressWriter) finish() {
	// Empty files never write, so never printed
	if p.total == 0 {
		p.percent = 100
		p.print()
	}
	fmt.Fprint(p.w, "\n")
}
