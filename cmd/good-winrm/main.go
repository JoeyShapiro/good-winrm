package main

import (
	"bufio"
	"context"
	"io"
	"os"

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

func main() {
	endpoint := winrm.NewEndpoint("localhost", 5985, false, false, nil, nil, nil, 0)
	params := winrm.DefaultParameters

	params.TransportDecorator = func() winrm.Transporter {
		return &winrm.ClientNTLM{}
	}

	client, err := winrm.NewClientWithParameters(endpoint, "Administrator", "", params)
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

	go io.Copy(os.Stdout, ps.Stdout)
	// go readStdout(ps.Stdout)
	go io.Copy(os.Stderr, ps.Stderr)

	reader := bufio.NewReader(os.Stdin)
	for {
		// raw doesnt play nice, and winrm prints after newline
		input, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		_, err = ps.Stdin.Write([]byte(input))
		if err != nil {
			break
		}
	}
}

func readStdout(stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	var line string
	input := "echo hello\n"
	for {
		r, _, err := reader.ReadRune()
		if err != nil {
			break
		}
		line += string(r)
		if r == '\n' {
			line = ""
		}

		// skip echoing input line
		var found bool
		for i := range min(len(input), len(line)) {
			if line[i] != input[i] {
				break
			}
		}

		if !found {
			os.Stdout.Write([]byte(string(r)))
		}
	}
}
