package main

import (
	"fmt"
	"os"
	"os/user"
	"strings"

	"golang.org/x/term"
)

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
