package good_serial

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	ioctlGetTermios = unix.TIOCGETA
	ioctlSetTermios = unix.TIOCSETA
)

var standardBauds = []int{
	50, 75, 110, 134, 150, 200, 300, 600, 1200, 1800, 2400, 4800, 7200,
	9600, 14400, 19200, 28800, 38400, 57600, 76800, 115200, 230400,
}

// setSpeed sets standard rates only; others need the IOSSIOSPEED ioctl.
func setSpeed(t *unix.Termios, baud int) error {
	for _, b := range standardBauds {
		if b == baud {
			// On macOS the speed fields are the rate itself
			t.Ispeed = uint64(baud)
			t.Ospeed = uint64(baud)
			return nil
		}
	}
	return unsupportedBaud(baud)
}

// Only the cu.* devices: the matching tty.* ones wait for carrier detect.
func listPorts() ([]string, error) {
	return filepath.Glob("/dev/cu.*")
}
