package good_ssh

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"

	"golang.org/x/crypto/ssh"

	"good-winrm/connection"
)

var (
	_ connection.Connection = (*SshConnection)(nil)
	_ connection.Resizer    = (*SshConnection)(nil)
	_ connection.Uploader   = (*SshConnection)(nil)
	_ connection.Downloader = (*SshConnection)(nil)
)

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
func NewSsh(host string, port int, username, command string, prompt Prompt) (*SshConnection, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	hostKeys, err := newHostKeyChecker(prompt)
	if err != nil {
		return nil, err
	}

	config := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.PasswordCallback(func() (string, error) {
				return prompt(fmt.Sprintf("%s@%s's password: ", username, host), false)
			}),
			// Servers using PAM usually ask for the password this way, with
			// their own prompt text
			ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i, q := range questions {
					if i == 0 && instruction != "" {
						q = instruction + "\n" + q
					}
					answer, err := prompt(q, echos[i])
					if err != nil {
						return nil, err
					}
					answers[i] = answer
				}
				return answers, nil
			}),
		},
		HostKeyCallback:   hostKeys.check,
		HostKeyAlgorithms: hostKeys.algorithms(addr),
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
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
