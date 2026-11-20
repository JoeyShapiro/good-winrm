package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"good-winrm/good_serial"
	"good-winrm/store"
)

func openStore() (*store.Store, error) {
	path, err := store.DefaultPath()
	if err != nil {
		return nil, err
	}
	return store.Open(path)
}

// rem add target <name> <winrm|ssh> <address> [--port N]
// rem add cred <name> <username>
func runAdd(args []string) error {
	fs := newFlagSet("rem add")
	port := fs.Int("port", 0, "Port (default 5985 for winrm, 22 for ssh)")
	positional := parseArgs(fs, args)
	if len(positional) == 0 {
		exitUsage(fs)
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	switch positional[0] {
	case "target", "targets":
		if len(positional) != 4 {
			exitUsage(fs)
		}
		t := store.Target{Name: positional[1], Type: positional[2], Address: positional[3], Port: *port}
		if err := checkName(t.Name); err != nil {
			return err
		}
		if t.Type == "serial" {
			return errors.New("serial devices aren't saved, since many devices share a port; use rem serial <device>")
		}
		if !slices.Contains(store.Types, t.Type) {
			return fmt.Errorf("unknown target type %q (want %s)", t.Type, strings.Join(store.Types, ", "))
		}
		return s.AddTarget(t)

	case "cred", "creds":
		if len(positional) != 3 {
			exitUsage(fs)
		}
		c := store.Cred{Name: positional[1], Username: positional[2]}
		if err := checkName(c.Name); err != nil {
			return err
		}
		// Before any passwords are typed
		if _, err := s.Cred(c.Name); err == nil {
			return fmt.Errorf("cred %q already exists", c.Name)
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}

		key, err := unlockVault(s)
		if err != nil {
			return err
		}
		// Asked for rather than taken as an argument, to keep it out of shell
		// history
		if c.Password, err = askNewPassword(fmt.Sprintf("password for %s", c.Username)); err != nil {
			return err
		}
		return s.AddCred(c, key)
	}
	exitUsage(fs)
	return nil
}

// rem remove <target|cred> <name>
func runRemove(args []string) error {
	fs := newFlagSet("rem remove")
	positional := parseArgs(fs, args)
	if len(positional) != 2 {
		exitUsage(fs)
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	switch positional[0] {
	case "target", "targets":
		return s.RemoveTarget(positional[1])
	case "cred", "creds":
		return s.RemoveCred(positional[1])
	}
	exitUsage(fs)
	return nil
}

// rem list
func runList(args []string) error {
	fs := newFlagSet("rem list")
	if len(parseArgs(fs, args)) != 0 {
		exitUsage(fs)
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	targets, err := s.Targets()
	if err != nil {
		return err
	}
	creds, err := s.Creds()
	if err != nil {
		return err
	}
	ports, err := good_serial.ListPorts()
	if err != nil {
		return err
	}

	var targetRows [][]string
	for _, t := range targets {
		port := t.Port
		if port == 0 {
			port = defaultPort(t.Type)
		}
		targetRows = append(targetRows, []string{t.Name, t.Type, t.Address, strconv.Itoa(port)})
	}
	var credRows [][]string
	for _, c := range creds {
		credRows = append(credRows, []string{c.Name, c.Username})
	}
	var portRows [][]string
	for _, p := range ports {
		portRows = append(portRows, []string{p})
	}

	printTable([]string{"TARGET", "TYPE", "ADDRESS", "PORT"}, targetRows, "(none, see rem add target)")
	fmt.Println()
	printTable([]string{"CRED", "USERNAME"}, credRows, "(none, see rem add cred)")
	fmt.Println()
	printTable([]string{"SERIAL PORT"}, portRows, "(none found)")
	return nil
}

// printTable prints aligned columns, or empty under the header if there
// are no rows.
func printTable(header []string, rows [][]string, empty string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, strings.Join(header, "\t"))
	if len(rows) == 0 {
		fmt.Fprintln(w, empty)
	}
	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	w.Flush()
}

func checkName(name string) error {
	switch {
	case name == "":
		return errors.New("name can't be empty")
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("name %q can't start with -", name)
	case strings.Contains(name, "@"):
		// It would read as user@target
		return fmt.Errorf("name %q can't contain @", name)
	case slices.Contains(reserved, name):
		return fmt.Errorf("%q is reserved (%s)", name, strings.Join(reserved, ", "))
	}
	if _, err := strconv.Atoi(name); err == nil {
		return fmt.Errorf("name %q can't be a number", name)
	}
	return nil
}
