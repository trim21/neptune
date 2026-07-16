// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package timestamp provides a time.Time wrapper that implements
// bencode.Marshaler and bencode.Unmarshaler. It serializes to a bencode string
// in RFC3339 UTC format, and deserializes from both bencode string (RFC3339)
// and bencode integer (Unix seconds).
package timestamp

import (
	"errors"
	"time"

	"github.com/trim21/go-bencode"
)

// Timestamp wraps time.Time with bencode serialization.
// Zero value is a valid timestamp (epoch).
type Timestamp struct {
	time.Time
}

// New creates a Timestamp from a time.Time.
func New(t time.Time) Timestamp {
	return Timestamp{Time: t}
}

// Now returns the current time as a Timestamp.
func Now() Timestamp {
	return Timestamp{Time: time.Now()}
}

// MarshalBencode encodes the timestamp as a bencode string in RFC3339 UTC format.
func (t Timestamp) MarshalBencode() ([]byte, error) {
	return bencode.Marshal(t.UTC().Format(time.RFC3339))
}

// UnmarshalBencode decodes a bencode value into a Timestamp.
// Supports both bencode integer (Unix seconds) and bencode string (RFC3339).
func (t *Timestamp) UnmarshalBencode(data []byte) error {
	if len(data) == 0 {
		return errors.New("timestamp: empty data")
	}
	if t == nil {
		return errors.New("timestamp: UnmarshalBencode on nil pointer")
	}

	// Try bencode string (RFC3339)
	var s string
	if err := bencode.Unmarshal(data, &s); err == nil {
		parsed, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return errors.New("timestamp: invalid RFC3339: " + err.Error())
		}
		t.Time = parsed.UTC()
		return nil
	}

	// Try bencode integer (Unix seconds)
	var v int64
	if err := bencode.Unmarshal(data, &v); err == nil {
		t.Time = time.Unix(v, 0).UTC()
		return nil
	}

	return errors.New("timestamp: expected bencode string or integer")
}

// IsZeroBencodeValue returns true if the timestamp is the zero value (epoch),
// supporting omitempty in bencode struct tags.
func (t Timestamp) IsZeroBencodeValue() bool {
	return t.IsZero()
}
