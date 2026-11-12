package connection

import "io"

// Connection is a remote session driven by a raw-mode terminal.
type Connection interface {
	// Send passes raw terminal input (keystrokes, pastes) to the remote.
	Send(p []byte) error
	// Receive blocks for the next chunk of output to show on the terminal.
	// It returns io.EOF once the remote session has ended.
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
