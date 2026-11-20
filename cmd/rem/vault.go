package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"

	"good-winrm/store"
)

// unlockVault asks for the rem password and returns the key to the creds.
func unlockVault(s *store.Store) (*store.Key, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, errors.New("stdin must be a terminal to enter the rem password")
	}

	has, err := s.HasVault()
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, errors.New("no rem password set, saved creds need one (set it with rem passwd)")
	}

	for attempt := 1; ; attempt++ {
		password, err := prompt("rem password: ", false)
		if err != nil {
			return nil, err
		}
		key, err := s.Unlock(password)
		if errors.Is(err, store.ErrWrongPassword) && attempt < passwordAttempts {
			fmt.Fprintln(os.Stderr, "Incorrect rem password, please try again.")
			continue
		}
		return key, err
	}
}

// rem passwd sets the rem password the first time, then changes it.
func runPasswd(args []string) error {
	fs := newFlagSet("rem passwd")
	if len(parseArgs(fs, args)) != 0 {
		exitUsage(fs)
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	has, err := s.HasVault()
	if err != nil {
		return err
	}
	if !has {
		fmt.Fprintln(os.Stderr, "The rem password encrypts every saved cred, and is asked for whenever one is used.")
		fmt.Fprintln(os.Stderr, "It can't be recovered if forgotten.")
		password, err := askNewPassword("new rem password")
		if err != nil {
			return err
		}
		_, err = s.CreateVault(password)
		return err
	}

	key, err := unlockVault(s)
	if err != nil {
		return err
	}
	password, err := askNewPassword("new rem password")
	if err != nil {
		return err
	}
	return s.ChangePassword(key, password)
}

// askNewPassword asks for a password twice, like passwd does.
func askNewPassword(what string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("stdin must be a terminal to enter the %s", what)
	}
	password, err := prompt(what+": ", false)
	if err != nil {
		return "", err
	}
	if password == "" {
		return "", fmt.Errorf("the %s can't be empty", what)
	}
	again, err := prompt(what+" again: ", false)
	if err != nil {
		return "", err
	}
	if password != again {
		return "", errors.New("passwords don't match")
	}
	return password, nil
}
