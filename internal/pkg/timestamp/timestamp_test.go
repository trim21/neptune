// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package timestamp

import (
	"strconv"
	"testing"
	"time"
)

func TestRoundtrip(t *testing.T) {
	orig := New(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC))

	b, err := orig.MarshalBencode()
	if err != nil {
		t.Fatal(err)
	}

	var restored Timestamp
	if err := restored.UnmarshalBencode(b); err != nil {
		t.Fatal(err)
	}

	if !orig.Equal(restored.Time) {
		t.Errorf("roundtrip mismatch: %v != %v", orig, restored)
	}
}

func TestUnmarshalInteger(t *testing.T) {
	orig := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	// Legacy integer format: i<unix_seconds>e
	data := []byte("i" + strconv.FormatInt(orig.Unix(), 10) + "e")

	var ts Timestamp
	if err := ts.UnmarshalBencode(data); err != nil {
		t.Fatal(err)
	}

	if !orig.Equal(ts.Time) {
		t.Errorf("expected %v, got %v", orig, ts)
	}
}

func TestUnmarshalString(t *testing.T) {
	// String format: <len>:<rfc3339>
	data := []byte("20:2024-01-15T10:30:00Z")

	var ts Timestamp
	if err := ts.UnmarshalBencode(data); err != nil {
		t.Fatal(err)
	}

	expected := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	if !ts.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, ts)
	}
}

func TestMarshalFormat(t *testing.T) {
	ts := New(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC))

	b, err := ts.MarshalBencode()
	if err != nil {
		t.Fatal(err)
	}

	expected := "20:2024-01-15T10:30:00Z"
	if string(b) != expected {
		t.Errorf("expected %q, got %q", expected, string(b))
	}
}

func TestZeroValue(t *testing.T) {
	var ts Timestamp
	if !ts.IsZero() {
		t.Error("zero Timestamp should have IsZero true")
	}
	if !ts.IsZeroBencodeValue() {
		t.Error("zero Timestamp should report IsZeroBencodeValue true")
	}
}
