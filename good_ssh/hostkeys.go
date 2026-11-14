package good_ssh

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// hostKeyChecker verifies host keys against ~/.ssh/known_hosts, asking
// before trusting a new host like ssh does.
type hostKeyChecker struct {
	path   string
	known  ssh.HostKeyCallback
	prompt Prompt
}

func newHostKeyChecker(prompt Prompt) (*hostKeyChecker, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, ".ssh", "known_hosts")

	// knownhosts.New fails on a missing file
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()

	known, err := knownhosts.New(path)
	if err != nil {
		return nil, err
	}
	return &hostKeyChecker{path: path, known: known, prompt: prompt}, nil
}

func (h *hostKeyChecker) check(hostname string, remote net.Addr, key ssh.PublicKey) error {
	err := h.known(hostname, remote, key)

	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) {
		return err
	}
	if len(keyErr.Want) > 0 {
		want := keyErr.Want[0]
		return fmt.Errorf("host key for %s has changed (%s key fingerprint is %s), known key is at %s:%d",
			hostname, key.Type(), ssh.FingerprintSHA256(key), want.Filename, want.Line)
	}

	question := fmt.Sprintf("The authenticity of host '%s' can't be established.\n"+
		"%s key fingerprint is %s.\n"+
		"Are you sure you want to continue connecting (yes/no)? ",
		hostname, key.Type(), ssh.FingerprintSHA256(key))
	answer, err := h.prompt(question, true)
	if err != nil {
		return err
	}
	if strings.TrimSpace(answer) != "yes" {
		return errors.New("host key verification failed")
	}

	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key))
	return err
}

// algorithms returns the host key algorithms for the key types already known
// for addr. Without this the server may present a different key type than
// the one on file, which looks like a changed key.
func (h *hostKeyChecker) algorithms(addr string) []string {
	// Checking a key that can't match makes knownhosts list the known ones
	placeholder, err := ssh.NewPublicKey(ed25519.PublicKey(make([]byte, ed25519.PublicKeySize)))
	if err != nil {
		return nil
	}
	err = h.known(addr, &net.TCPAddr{IP: net.IPv4zero}, placeholder)

	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) {
		return nil
	}

	var algos []string
	for _, k := range keyErr.Want {
		names := []string{k.Key.Type()}
		if names[0] == ssh.KeyAlgoRSA {
			names = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
		}
		for _, name := range names {
			if !slices.Contains(algos, name) {
				algos = append(algos, name)
			}
		}
	}
	return algos
}
