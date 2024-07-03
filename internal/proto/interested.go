package proto

import (
	"io"
)

func SendInterested(w io.Writer) error {
	_, err := w.Write([]byte{0, 0, 0, 1, byte(Interested)})
	return err
}

func SendNotInterested(w io.Writer) error {
	_, err := w.Write([]byte{0, 0, 0, 1, byte(NotInterested)})
	return err
}
