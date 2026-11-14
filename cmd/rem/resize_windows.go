package main

import (
	"time"

	"golang.org/x/term"
)

// watchResize calls resize with the terminal's size now and on every change.
// Windows has no SIGWINCH, so the size is polled.
func watchResize(fd int, resize func(width, height int)) {
	var lastWidth, lastHeight int
	for {
		if width, height, err := term.GetSize(fd); err == nil && width > 0 && height > 0 && (width != lastWidth || height != lastHeight) {
			lastWidth, lastHeight = width, height
			resize(width, height)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
