package good_serial

import (
	"io"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var standardBauds = []int{
	110, 300, 600, 1200, 2400, 4800, 9600, 14400, 19200, 38400, 57600,
	115200, 128000, 256000,
}

const maxDword = 0xFFFFFFFF

// DCB.Flags bits
const (
	dcbBinary    = 1 << 0
	dcbDtrEnable = 1 << 4  // fDtrControl = DTR_CONTROL_ENABLE
	dcbRtsEnable = 1 << 12 // fRtsControl = RTS_CONTROL_ENABLE
)

type port struct {
	handle windows.Handle
}

func openPort(device string, baud int) (io.ReadWriteCloser, error) {
	if !slices.Contains(standardBauds, baud) {
		return nil, unsupportedBaud(baud)
	}

	// COM10 and up only open through the device namespace
	if !strings.HasPrefix(device, `\\.\`) {
		device = `\\.\` + device
	}
	name, err := windows.UTF16PtrFromString(device)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, err
	}

	if err := configure(handle, baud); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	return &port{handle: handle}, nil
}

func configure(handle windows.Handle, baud int) error {
	var dcb windows.DCB
	dcb.DCBlength = uint32(unsafe.Sizeof(dcb))
	if err := windows.GetCommState(handle, &dcb); err != nil {
		return err
	}

	// 8N1, no flow control, DTR and RTS held on like other serial terminals
	dcb.BaudRate = uint32(baud)
	dcb.ByteSize = 8
	dcb.Parity = 0   // NOPARITY
	dcb.StopBits = 0 // ONESTOPBIT
	dcb.Flags = dcbBinary | dcbDtrEnable | dcbRtsEnable
	if err := windows.SetCommState(handle, &dcb); err != nil {
		return err
	}

	// Reads wait for the first byte, then return whatever has arrived
	return windows.SetCommTimeouts(handle, &windows.CommTimeouts{
		ReadIntervalTimeout:        maxDword,
		ReadTotalTimeoutMultiplier: maxDword,
		ReadTotalTimeoutConstant:   maxDword - 1,
	})
}

func (p *port) Read(b []byte) (int, error) {
	var n uint32
	err := windows.ReadFile(p.handle, b, &n, nil)
	return int(n), err
}

func (p *port) Write(b []byte) (int, error) {
	var n uint32
	err := windows.WriteFile(p.handle, b, &n, nil)
	return int(n), err
}

func (p *port) Close() error {
	// A blocked ReadFile would otherwise keep CloseHandle waiting
	windows.CancelIoEx(p.handle, nil)
	return windows.CloseHandle(p.handle)
}
