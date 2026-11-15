package good_serial

import (
	"errors"
	"fmt"
	"io"

	"good-winrm/connection"
)

var _ connection.Connection = (*SerialConnection)(nil)

// SerialConnection talks to a serial device (a UART, or a USB CDC-ACM device
// like /dev/cu.usbmodem*). The device does its own echoing, so input is
// passed straight through, including Ctrl+C. Ports are opened as 8N1 with no
// flow control.
type SerialConnection struct {
	port   io.ReadWriteCloser
	output *connection.Output
}

func NewSerial(device string, baud int) (*SerialConnection, error) {
	port, err := openPort(device, baud)
	if err != nil {
		return nil, err
	}
	return &SerialConnection{
		port:   port,
		output: connection.NewOutput(connection.Stream{Reader: port}),
	}, nil
}

func (c *SerialConnection) Send(p []byte) error {
	_, err := c.port.Write(p)
	return err
}

// Receive only ends if the device goes away, such as being unplugged.
func (c *SerialConnection) Receive() ([]byte, error) {
	return c.output.Receive()
}

func (c *SerialConnection) Close() error {
	return c.port.Close()
}

func unsupportedBaud(baud int) error {
	return fmt.Errorf("baud rate %d: %w (non-standard rates are unimplemented)", baud, errors.ErrUnsupported)
}
