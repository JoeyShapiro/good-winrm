//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// watchResize calls resize with the terminal's size now and on every change.
func watchResize(fd int, resize func(width, height int)) {
	changed := make(chan os.Signal, 1)
	signal.Notify(changed, syscall.SIGWINCH)
	for {
		if width, height, err := term.GetSize(fd); err == nil && width > 0 && height > 0 {
			resize(width, height)
		}
		<-changed
	}
}
