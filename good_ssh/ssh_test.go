package good_ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"good-winrm/connection"
)

const testPassword = "right"

// testServer accepts testPassword by either password or keyboard-interactive
// auth, like servers using PAM, and accepts any session after that.
func testServer(t *testing.T) (host string, port int) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) == testPassword {
				return nil, nil
			}
			return nil, errors.New("wrong password")
		},
		KeyboardInteractiveCallback: func(_ ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			answers, err := challenge("", "", []string{"Password: "}, []bool{false})
			if err != nil {
				return nil, err
			}
			if len(answers) == 1 && answers[0] == testPassword {
				return nil, nil
			}
			return nil, errors.New("wrong password")
		},
	}
	config.AddHostKey(signer)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go serve(conn, config)
		}
	}()
	addr := l.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func serve(conn net.Conn, config *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		channel, requests, err := ch.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range requests {
				req.Reply(true, nil)
			}
			channel.Close()
		}()
	}
}

// answering returns a Prompt that trusts the host key and gives passwords in
// order, recording every password question asked.
func answering(passwords ...string) (Prompt, *[]string) {
	var asked []string
	return func(question string, echo bool) (string, error) {
		if echo {
			return "yes", nil
		}
		asked = append(asked, question)
		if len(passwords) == 0 {
			return "", errors.New("asked too many times")
		}
		p := passwords[0]
		passwords = passwords[1:]
		return p, nil
	}, &asked
}

func TestPasswordAttempts(t *testing.T) {
	cases := []struct {
		name      string
		attempts  int
		passwords []string
		asks      int
		ok        bool
	}{
		{"right first", 3, []string{testPassword}, 1, true},
		{"right third", 3, []string{"a", "b", testPassword}, 3, true},
		{"all wrong", 3, []string{"a", "b", "c"}, 3, false},
		{"one attempt", 1, []string{"a"}, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Keep the test's host key out of the real known_hosts
			t.Setenv("HOME", t.TempDir())
			host, port := testServer(t)
			prompt, asked := answering(tc.passwords...)

			c, err := NewSsh(host, port, "user", "", prompt, tc.attempts)
			if tc.ok && err != nil {
				t.Fatal(err)
			}
			if !tc.ok && !errors.Is(err, connection.ErrUnauthorized) {
				t.Fatalf("got %v, want ErrUnauthorized", err)
			}
			if c != nil {
				c.Close()
			}

			if len(*asked) != tc.asks {
				t.Fatalf("asked %d times, want %d: %q", len(*asked), tc.asks, *asked)
			}
			for i, q := range *asked {
				if retried := strings.HasPrefix(q, "Permission denied"); retried != (i > 0) {
					t.Errorf("question %d is %q", i+1, q)
				}
			}
		})
	}
}
