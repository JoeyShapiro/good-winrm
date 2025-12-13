package main

import (
	"bufio"
	"context"
	"io"
	"os"

	"github.com/masterzen/winrm"
)

func main() {
	endpoint := winrm.NewEndpoint("hostname", 5985, false, false, nil, nil, nil, 0)
	params := winrm.DefaultParameters

	params.TransportDecorator = func() winrm.Transporter {
		return &winrm.ClientNTLM{}
	}

	client, err := winrm.NewClientWithParameters(endpoint, "username", "password", params)
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
	go io.Copy(os.Stderr, ps.Stderr)

	reader := bufio.NewReader(os.Stdin)
	for {
		input, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		_, err = ps.Stdin.Write([]byte(input))
		if err != nil {
			break
		}
		// io.Copy(ps.Stdin, bytes.NewBufferString(input))
	}
}
