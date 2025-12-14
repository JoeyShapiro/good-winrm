package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/masterzen/winrm"
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
	host := flag.String("host", "localhost", "WinRM host")
	port := flag.Int("port", 5985, "WinRM port")
	username := flag.String("username", "Administrator", "WinRM username")
	password := flag.String("password", "", "WinRM password")
	command := flag.String("command", "powershell.exe", "Command to execute")

	flag.Parse()

	endpoint := winrm.NewEndpoint(*host, *port, false, false, nil, nil, nil, 0)
	params := winrm.DefaultParameters

	params.TransportDecorator = func() winrm.Transporter {
		return &winrm.ClientNTLM{}
	}

	client, err := winrm.NewClientWithParameters(endpoint, *username, *password, params)
	if err != nil {
		panic(err)
	}

	shell, err := client.CreateShell()
	if err != nil {
		panic(err)
	}
	defer shell.Close()

	ps, err := shell.ExecuteWithContext(context.Background(), *command)
	if err != nil {
		panic(err)
	}
	defer ps.Close()

	go readStdout(ps.Stdout)
	go io.Copy(os.Stderr, ps.Stderr)

	// Create channel to receive signals
	sigChan := make(chan os.Signal, 1)

	// Register for SIGINT (Ctrl+C)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGINT)

	go func() {
		<-sigChan
		// TODO change to something less common
		// TODO how can i send this to the remote powershell?
		// TODO maybe just do double tap
		// Meta Terminal on Ctrl+C
		state.IsMetaTerminal = true
		fmt.Printf("\n\033[32m(good-winrm)\033[0m ")
	}()

	reader := bufio.NewReader(os.Stdin)
	for {
		state.Input, err = reader.ReadString('\n')
		if err == io.EOF {
			if state.IsMetaTerminal {
				state.IsMetaTerminal = false
				fmt.Printf("\n")
				continue
			} else {
				break
			}
		} else if err != nil {
			panic(err)
		}
		state.Commanded = true

		if state.IsMetaTerminal {
			err := evalMetaCommand()
			if err != nil {
				fmt.Printf("Error: %v\n", err)
			}
			if state.IsMetaTerminal {
				fmt.Printf("\033[32m(good-winrm)\033[0m ")
			}
			state.Commanded = false
		} else {
			_, err = ps.Stdin.Write([]byte(state.Input))
			if err != nil {
				break
			}
		}
	}
}

type State struct {
	IsMetaTerminal bool
	Input          string
	Commanded      bool
}

func evalMetaCommand() error {
	if state.Input == "" {
		return nil
	}

	switch state.Input {
	case "exit":
		state.IsMetaTerminal = false
	case "upload":
		// TODO
	case "download":
		// TODO
	default:
		return errors.New("Unknown meta command: " + state.Input)
	}
	return nil
}

func readStdout(stdout io.Reader) {
	var line string
	reader := bufio.NewReader(stdout)
	for {
		r, _, err := reader.ReadRune()
		if err == io.EOF {
			os.Exit(0)
		} else if err != nil {
			panic(err)
		}

		line += string(r)
		n := min(len(line), len(state.Input))
		if state.Commanded && len(line) == len(state.Input) && line[:n] == state.Input[:n] {
			state.Commanded = false
			line = ""
		} else if !state.Commanded || line[:n] != state.Input[:n] {
			os.Stdout.Write([]byte(line))
			os.Stdout.Sync()
			line = ""
		}
	}
}
