package main

import (
	"errors"

	"github.com/google/shlex"

	"good-winrm/connection"
)

// errQuit ends the session from the meta terminal.
var errQuit = errors.New("quit")

const metaPrompt = "\033[32m(rem)\033[0m "

func (t *terminal) evalMetaCommand(input string) error {
	args, err := shlex.Split(input)
	if err != nil {
		return err
	}

	if len(args) == 0 {
		return nil
	}

	switch args[0] {
	case "exit":
		if !t.connected {
			return errors.New("not connected (reconnect to try again, quit to exit)")
		}
		t.meta = false
	case "quit", "bye":
		return errQuit
	case "reconnect":
		if t.connected {
			return errors.New("already connected")
		}
		t.startReconnecting()
	case "upload":
		if len(args) != 3 {
			return errors.New("usage: upload <local_path> <remote_path>")
		}
		if !t.connected {
			return errors.New("upload: not connected")
		}
		u, ok := t.conn.(connection.Uploader)
		if !ok {
			return errors.New("upload is not supported by this connection")
		}
		return u.Upload(args[1], args[2], t.screen)
	case "download":
		if len(args) != 3 {
			return errors.New("usage: download <remote_path> <local_path>")
		}
		if !t.connected {
			return errors.New("download: not connected")
		}
		d, ok := t.conn.(connection.Downloader)
		if !ok {
			return errors.New("download is not supported by this connection")
		}
		return d.Download(args[1], args[2], t.screen)
	default:
		return errors.New("unknown meta command: " + args[0])
	}
	return nil
}
