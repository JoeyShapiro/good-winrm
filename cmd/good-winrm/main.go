package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

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

var (
	state State
)

func main() {
	endpoint := winrm.NewEndpoint("localhost", 5985, false, false, nil, nil, nil, 0)
	params := winrm.DefaultParameters

	params.TransportDecorator = func() winrm.Transporter {
		return &winrm.ClientNTLM{}
	}

	client, err := winrm.NewClientWithParameters(endpoint, "Administrator", os.Args[1], params)
	if err != nil {
		panic(err)
	}

	shell, err := client.CreateShell()
	if err != nil {
		panic(err)
	}
	defer shell.Close()

	ps, err := shell.ExecuteWithContext(context.Background(), "powershell.exe")
	if err != nil {
		panic(err)
	}
	defer ps.Close()

	// Save original terminal state
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		panic(err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	go readStdout(ps.Stdout)
	go io.Copy(os.Stderr, ps.Stderr)

	reader := bufio.NewReader(os.Stdin)
	for {
		// want gdb style
		r, _, err := reader.ReadRune()
		if err != nil {
			break
		}
		s := string(r)

		// Convert CR to CRLF
		if r == '\r' {
			os.Stdin.Write([]byte(s))
			r = '\n'
			s = string(r)
		}

		// Meta Terminal on Ctrl+C
		if r == '\x03' {
			state.IsMetaTerminal = true
			fmt.Printf("\r\n\033[31m(good-winrm)\033[0m ")
			continue
		}

		if state.IsMetaTerminal {
			os.Stdin.Write([]byte(s))
			if r == '\n' {
				err := evalMetaCommand()
				if err != nil {
					fmt.Printf("Error: %v\r\n", err)
				}
				state.MetaCommand = ""
				if state.IsMetaTerminal {
					fmt.Printf("\033[31m(good-winrm)\033[0m ")
				}
			} else {
				state.MetaCommand += s
			}
		} else {
			_, err = ps.Stdin.Write([]byte(s))
			if err != nil {
				break
			}
		}

		// Break on Ctrl+D
		if r == '\x04' {
			break
		}
	}
}

type State struct {
	IsMetaTerminal bool
	MetaCommand    string
}

func evalMetaCommand() error {
	if state.MetaCommand == "" {
		return nil
	}

	switch state.MetaCommand {
	case "exit":
		state.IsMetaTerminal = false
	default:
		return errors.New("Unknown meta command: " + state.MetaCommand)
	}
	return nil
}

func readStdout(stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	for {
		r, _, err := reader.ReadRune()
		if err != nil {
			break
		}

		if r == '\n' {
			os.Stdout.Write([]byte{'\r'})
		}

		os.Stdout.Write([]byte(string(r)))
		os.Stdout.Sync()
	}
}
