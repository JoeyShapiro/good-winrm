//go:build !darwin && !linux && !windows

package good_serial

import (
	"errors"
	"io"
	"runtime"
)

func openPort(device string, baud int) (io.ReadWriteCloser, error) {
	return nil, errors.New("serial ports are unimplemented on " + runtime.GOOS)
}

func listPorts() ([]string, error) {
	return nil, nil
}
