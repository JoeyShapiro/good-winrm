package main

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// looksLikeHost reports whether name is an IP address or is in the hosts
// file. It doesn't ask DNS, which would be slow and would match bare names
// through search domains.
func looksLikeHost(name string) bool {
	if net.ParseIP(name) != nil {
		return true
	}
	f, err := os.Open(hostsPath())
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line, _, _ := strings.Cut(scanner.Text(), "#")
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// The first field is the address, the rest are its names
		for _, host := range fields[1:] {
			if strings.EqualFold(host, name) {
				return true
			}
		}
	}
	return false
}

func hostsPath() string {
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return filepath.Join(root, "System32", "drivers", "etc", "hosts")
	}
	return "/etc/hosts"
}
