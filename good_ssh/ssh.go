package good_ssh

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"good-winrm/connection"
)

var (
	_ connection.Connection = (*SshConnection)(nil)
	_ connection.Resizer    = (*SshConnection)(nil)
	_ connection.Uploader   = (*SshConnection)(nil)
	_ connection.Downloader = (*SshConnection)(nil)
)

var errOutOfAttempts = errors.New("out of password attempts")

// Prompt asks the user a question while connecting, before the terminal is
// in raw mode. echo is false for secrets such as passwords.
type Prompt func(question string, echo bool) (string, error)

// SshConnection runs a shell or command in a remote terminal (PTY). The
// remote terminal does all line editing and echoing, so input is passed
// straight through, including Ctrl+C.
type SshConnection struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	output  *connection.Output
}

// NewSsh connects with password or keyboard-interactive auth, checking the
// host key against ~/.ssh/known_hosts. An empty command starts a login shell.
// The password is asked for up to attempts times, across both ways of
// asking.
func NewSsh(host string, port int, username, command string, prompt Prompt, attempts int) (*SshConnection, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	hostKeys, err := newHostKeyChecker(prompt)
	if err != nil {
		return nil, err
	}

	// Counts each time hidden answers are asked for, by either method
	asked := 0
	askHidden := func() error {
		if asked == attempts {
			// Makes the client give up on this method, and so the login
			return errOutOfAttempts
		}
		asked++
		return nil
	}
	retry := func(question string) string {
		if asked > 1 {
			return "Permission denied, please try again.\n" + question
		}
		return question
	}

	config := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.RetryableAuthMethod(ssh.PasswordCallback(func() (string, error) {
				if err := askHidden(); err != nil {
					return "", err
				}
				return prompt(retry(fmt.Sprintf("%s@%s's password: ", username, host)), false)
			}), attempts),
			// Servers using PAM usually ask for the password this way, with
			// their own prompt text
			ssh.RetryableAuthMethod(ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
				// Servers can send messages with no questions at all
				if slices.Contains(echos, false) {
					if err := askHidden(); err != nil {
						return nil, err
					}
				}
				answers := make([]string, len(questions))
				for i, q := range questions {
					if i == 0 && instruction != "" {
						q = instruction + "\n" + q
					}
					if i == 0 {
						q = retry(q)
					}
					answer, err := prompt(q, echos[i])
					if err != nil {
						return nil, err
					}
					answers[i] = answer
				}
				return answers, nil
			}), attempts),
		},
		HostKeyCallback:   hostKeys.check,
		HostKeyAlgorithms: hostKeys.algorithms(addr),
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil && (errors.Is(err, errOutOfAttempts) || strings.Contains(err.Error(), "unable to authenticate")) {
		return nil, connection.ErrUnauthorized
	} else if err != nil {
		return nil, err
	}

	c, err := start(client, command)
	if err != nil {
		client.Close()
		return nil, err
	}
	return c, nil
}

func start(client *ssh.Client, command string) (*SshConnection, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}

	termType := os.Getenv("TERM")
	if termType == "" {
		termType = "xterm-256color"
	}
	// The real size is set by Resize once the local terminal is known
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty(termType, 24, 80, modes); err != nil {
		session.Close()
		return nil, err
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		session.Close()
		return nil, err
	}

	if command == "" {
		err = session.Shell()
	} else {
		err = session.Start(command)
	}
	if err != nil {
		session.Close()
		return nil, err
	}

	return &SshConnection{
		client:  client,
		session: session,
		stdin:   stdin,
		output: connection.NewOutput(
			connection.Stream{Reader: stdout},
			connection.Stream{Reader: stderr},
		),
	}, nil
}

func (c *SshConnection) Send(p []byte) error {
	_, err := c.stdin.Write(p)
	return err
}

func (c *SshConnection) Receive() ([]byte, error) {
	return c.output.Receive()
}

func (c *SshConnection) Resize(width, height int) error {
	return c.session.WindowChange(height, width)
}

func (c *SshConnection) Close() error {
	err := c.session.Close()
	if errors.Is(err, io.EOF) {
		// Already closed by the remote exiting
		err = nil
	}
	return errors.Join(err, c.client.Close())
}
