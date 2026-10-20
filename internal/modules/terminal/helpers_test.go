package terminal

import (
	"bufio"
	"bytes"
)

type bytesBuffer = bytes.Buffer

func newReader(b *bytes.Buffer) *bufio.Reader { return bufio.NewReader(b) }
