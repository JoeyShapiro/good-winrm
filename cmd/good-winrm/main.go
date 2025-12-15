package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/google/shlex"
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

	var err error
	state.Client, err = winrm.NewClientWithParameters(endpoint, *username, *password, params)
	if err != nil {
		panic(err)
	}

	shell, err := state.Client.CreateShell()
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
				fmt.Printf("error: %v\n", err)
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
	Client         *winrm.Client
}

func evalMetaCommand() error {
	if state.Input == "" {
		return nil
	}

	args, err := shlex.Split(state.Input)
	if err != nil {
		return err
	}

	if len(args) == 0 {
		return nil
	}

	switch args[0] {
	case "exit":
		state.IsMetaTerminal = false
	case "upload":
		if len(args) != 3 {
			err = errors.New("usage: upload <local_path> <remote_path>")
		} else {
			err = uploadFile(state.Client, args[1], args[2])
		}
	case "download":
		if len(args) != 3 {
			err = errors.New("usage: download <remote_path> <local_path>")
		} else {
			err = downloadFile(state.Client, args[1], args[2])
		}
	default:
		err = errors.New("unknown meta command: " + state.Input)
	}
	return err
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

func uploadFile(client *winrm.Client, localPath, remotePath string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}

	// First, delete if exists
	_, err = client.RunWithContext(context.Background(), fmt.Sprintf("Remove-Item -Path '%s' -Force -ErrorAction SilentlyContinue", remotePath), nil, nil)
	if err != nil {
		return err
	}

	// Chunk size (stay well under 8KB command limit)
	chunkSize := 5000

	/*
		# Create the file stream
		$stream = [System.IO.File]::Create('C:\target\file.exe')

		# For each chunk (sent as separate commands):
		$bytes = [System.Convert]::FromBase64String('ENCODED_CHUNK')
		$stream.Write($bytes, 0, $bytes.Length)

		# Finally:
		$stream.Close()
	*/

	for i := 0; i < len(data); i += chunkSize {
		end := min(i+chunkSize, len(data))

		chunk := data[i:end]
		encoded := base64.StdEncoding.EncodeToString(chunk)

		// Append each chunk
		psCommand := fmt.Sprintf(`
            $data = [System.Convert]::FromBase64String('%s')
            Add-Content -Path '%s' -Value $data -Encoding Byte
        `, encoded, remotePath)

		_, err = client.RunWithContext(context.Background(), psCommand, os.Stdout, os.Stderr)
		if err != nil {
			return err
		}
		fmt.Printf("%s: %d%% (%d/%d) bytes uploaded\n", remotePath, end*100/len(data), end, len(data))
	}

	return nil
}

func downloadFile(client *winrm.Client, remotePath, localPath string) error {
	// Read and encode on remote side
	psCommand := fmt.Sprintf(`
        $data = [System.IO.File]::ReadAllBytes('%s')
        [System.Convert]::ToBase64String($data)
    `, remotePath)

	var stdout bytes.Buffer
	_, err := client.RunWithContext(context.Background(), psCommand, &stdout, os.Stderr)
	if err != nil {
		return err
	}

	// Decode
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stdout.String()))
	if err != nil {
		return err
	}

	// Write locally
	return os.WriteFile(localPath, decoded, 0600)
}
