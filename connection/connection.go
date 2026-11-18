package connection

import (
	"errors"
	"io"
)

// ErrDisconnected is returned (wrapped) by Send or Receive when the
// connection was lost, as opposed to the remote ending the session, and
// reconnecting is worth trying.
var ErrDisconnected = errors.New("disconnected")

// ErrUnauthorized is returned (wrapped) when connecting if the server
// rejects the username or password.
var ErrUnauthorized = errors.New("permission denied (wrong username or password)")

// Connection is a remote session driven by a raw-mode terminal.
type Connection interface {
	// Send passes raw terminal input (keystrokes, pastes) to the remote.
	Send(p []byte) error
	// Receive blocks for the next chunk of output to show on the terminal.
	// It returns io.EOF once the remote session has ended, or an error
	// wrapping ErrDisconnected if the connection was lost.
	Receive() ([]byte, error)
	Close() error
}

// Uploader is a Connection that can copy local files to the remote.
type Uploader interface {
	Upload(localPath, remotePath string, progress io.Writer) error
}

// Downloader is a Connection that can copy remote files to local.
type Downloader interface {
	Download(remotePath, localPath string, progress io.Writer) error
}

// Resizer is a Connection with a remote terminal that should follow the
// local terminal's size.
type Resizer interface {
	Resize(width, height int) error
}
