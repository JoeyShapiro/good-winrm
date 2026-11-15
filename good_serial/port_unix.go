//go:build darwin || linux

package good_serial

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func openPort(device string, baud int) (io.ReadWriteCloser, error) {
	// Non-blocking so the open doesn't wait for carrier detect. It also lets
	// os.File use the poller, so Close interrupts a blocked Read.
	fd, err := unix.Open(device, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: device, Err: err}
	}

	if err := configure(fd, baud); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), device), nil
}

func configure(fd, baud int) error {
	t, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return err
	}

	// Raw mode
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN

	// 8N1, no hardware flow control, ignore modem control lines
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.CSTOPB | unix.CRTSCTS
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL

	// Reads return as soon as one byte is available
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0

	if err := setSpeed(t, baud); err != nil {
		return err
	}
	return unix.IoctlSetTermios(fd, ioctlSetTermios, t)
}
