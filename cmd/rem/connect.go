package main

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"strings"

	"golang.org/x/term"

	"good-winrm/connection"
	"good-winrm/good_serial"
	"good-winrm/good_ssh"
	"good-winrm/good_winrm"
	"good-winrm/store"
)

// connInfo is everything needed to connect, from the command line, a saved
// target and a saved cred.
type connInfo struct {
	kind     string // winrm, ssh or serial
	address  string // host, or device path for serial
	port     int    // 0 for the default; the baud rate for serial
	username string
	password string // empty to prompt for it
	command  string
}

func defaultPort(kind string) int {
	switch kind {
	case "winrm":
		return 5985
	case "ssh":
		return 22
	case "serial":
		return 115200
	}
	return 0
}

func runConnect(args []string) error {
	fs := newFlagSet("rem")
	port := fs.Int("port", 0, "Port (default 5985 for winrm, 22 for ssh)")
	command := fs.String("command", "", "Command to execute (default powershell.exe for winrm, a login shell for ssh)")
	baud := fs.Int("baud", 0, "Baud rate for serial (default 115200)")
	credName := fs.String("cred", "", "Saved cred to log in with")
	userFlag := fs.String("user", "", "Username to log in with, prompting for the password")
	positional := parseArgs(fs, args)

	var c connInfo
	var name string
	explicitUser := false
	switch len(positional) {
	case 1:
		name = positional[0]
		var username string
		if i := strings.LastIndex(name, "@"); i >= 0 {
			username, name = name[:i], name[i+1:]
			if username == "" || name == "" {
				return fmt.Errorf("invalid destination: %q", positional[0])
			}
			// Leaves room for rem user@host to mean a host that isn't saved
			if looksLikeHost(name) {
				return fmt.Errorf("%q is ambiguous: it's a host as well as a target name; use rem %s --user %s, or rem <winrm|ssh> %s", positional[0], name, username, positional[0])
			}
			explicitUser = true
		}
		t, err := savedTarget(name)
		if err != nil {
			return err
		}
		c = connInfo{kind: t.Type, address: t.Address, port: t.Port, username: username}
	case 2:
		kind, destination := positional[0], positional[1]
		name = destination
		c = connInfo{kind: kind, address: destination}
		switch kind {
		case "winrm", "ssh":
			explicitUser = strings.Contains(destination, "@")
			var err error
			if c.username, c.address, err = parseDestination(destination); err != nil {
				return err
			}
		case "serial":
		default:
			exitUsage(fs)
		}
	default:
		exitUsage(fs)
	}

	if *userFlag != "" {
		if c.kind == "serial" {
			return errors.New("serial connections don't log in, so --user doesn't apply")
		}
		if explicitUser {
			return errors.New("give either user@host or --user, not both")
		}
		if *credName != "" {
			return errors.New("give either --cred or --user, not both")
		}
		c.username = *userFlag
	} else if *credName != "" {
		if c.kind == "serial" {
			return errors.New("serial connections don't log in, so --cred doesn't apply")
		}
		if explicitUser {
			return errors.New("give either user@host or --cred, not both")
		}
		cred, err := savedCred(*credName)
		if err != nil {
			return err
		}
		c.username, c.password = cred.Username, cred.Password
	} else if c.username == "" && c.kind != "serial" {
		// Like ssh, default to the local username
		u, err := user.Current()
		if err != nil {
			return err
		}
		c.username = u.Username
	}

	// Flags override what was saved
	if c.kind == "serial" && *baud != 0 {
		c.port = *baud
	} else if c.kind != "serial" && *port != 0 {
		c.port = *port
	}
	if c.port == 0 {
		c.port = defaultPort(c.kind)
	}
	c.command = *command

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("stdin must be a terminal")
	}

	dial, err := c.dialer()
	if err != nil {
		return err
	}
	return runSession(dial, name)
}

func savedTarget(name string) (store.Target, error) {
	s, err := openStore()
	if err != nil {
		return store.Target{}, err
	}
	defer s.Close()

	t, err := s.Target(name)
	if errors.Is(err, store.ErrNotFound) {
		return t, fmt.Errorf("no saved target named %q (see rem list)", name)
	}
	return t, err
}

func savedCred(name string) (store.Cred, error) {
	s, err := openStore()
	if err != nil {
		return store.Cred{}, err
	}
	defer s.Close()

	c, err := s.Cred(name)
	if errors.Is(err, store.ErrNotFound) {
		return c, fmt.Errorf("no saved cred named %q (see rem list)", name)
	}
	return c, err
}

// dialer asks for anything still missing, then returns a function that
// connects, and can be called again to reconnect.
func (c connInfo) dialer() (func() (connection.Connection, error), error) {
	switch c.kind {
	case "winrm":
		command := c.command
		if command == "" {
			command = "powershell.exe"
		}
		password := c.password
		if password == "" {
			var err error
			password, err = prompt(fmt.Sprintf("%s@%s's password: ", c.username, c.address), false)
			if err != nil {
				return nil, err
			}
		}
		return func() (connection.Connection, error) {
			return good_winrm.NewWinrm(c.address, c.port, c.username, password, command)
		}, nil
	case "ssh":
		ask := good_ssh.Prompt(prompt)
		if c.password != "" {
			// Answer hidden questions (the password, whichever way the server
			// asks) with the saved password; still ask about host keys
			ask = func(question string, echo bool) (string, error) {
				if !echo {
					return c.password, nil
				}
				return prompt(question, echo)
			}
		}
		return func() (connection.Connection, error) {
			return good_ssh.NewSsh(c.address, c.port, c.username, c.command, ask)
		}, nil
	case "serial":
		return func() (connection.Connection, error) {
			return good_serial.NewSerial(c.address, c.port)
		}, nil
	}
	return nil, fmt.Errorf("unknown target type %q", c.kind)
}
