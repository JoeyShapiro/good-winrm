package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"good-winrm/connection"
	"good-winrm/good_serial"
	"good-winrm/good_ssh"
	"good-winrm/good_winrm"
)

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
