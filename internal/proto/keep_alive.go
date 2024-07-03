package proto

import (
	"io"
)

var keepAlive = []byte{0, 0, 0, 0}

func SendKeepAlive(w io.Writer) error {
	_, err := w.Write(keepAlive)
	return err
}
