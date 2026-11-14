package connection

import (
	"io"
	"sync"
)

// Stream is a remote output stream. Transform, if set, rewrites each chunk
// before it is shown and must return a slice it doesn't reuse.
type Stream struct {
	Reader    io.Reader
	Transform func([]byte) []byte
}

// Output merges remote streams and locally generated output into one
// sequence of chunks, for implementing Receive.
type Output struct {
	ch   chan []byte
	done chan struct{} // closed once every stream has ended
}

func NewOutput(streams ...Stream) *Output {
	o := &Output{
		ch:   make(chan []byte, 16),
		done: make(chan struct{}),
	}

	var wg sync.WaitGroup
	for _, s := range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o.pump(s)
		}()
	}
	go func() {
		wg.Wait()
		close(o.done)
	}()
	return o
}

func (o *Output) pump(s Stream) {
	buf := make([]byte, 4096)
	for {
		n, err := s.Reader.Read(buf)
		if n > 0 {
			var p []byte
			if s.Transform != nil {
				p = s.Transform(buf[:n])
			} else {
				p = append([]byte(nil), buf[:n]...)
			}
			if len(p) > 0 {
				o.ch <- p
			}
		}
		if err != nil {
			return
		}
	}
}

// Emit queues locally generated output, such as the echo of typed keys.
func (o *Output) Emit(p []byte) {
	if len(p) == 0 {
		return
	}
	select {
	case o.ch <- p:
	case <-o.done:
	}
}

// Receive returns the next chunk, or io.EOF once every stream has ended.
func (o *Output) Receive() ([]byte, error) {
	select {
	case p := <-o.ch:
		return p, nil
	case <-o.done:
		// Drain anything queued before the streams ended
		select {
		case p := <-o.ch:
			return p, nil
		default:
			return nil, io.EOF
		}
	}
}
