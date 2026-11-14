package lineedit

// CRLF turns bare "\n" into "\r\n", which a raw-mode terminal no longer does.
// Only use it on output with no terminal behind it: full-screen programs
// over a real terminal rely on a bare "\n" moving the cursor straight down.
type CRLF struct {
	last byte
}

// Convert returns a converted copy of p.
func (c *CRLF) Convert(p []byte) []byte {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		if b == '\n' && c.last != '\r' {
			out = append(out, '\r')
		}
		out = append(out, b)
		c.last = b
	}
	return out
}
