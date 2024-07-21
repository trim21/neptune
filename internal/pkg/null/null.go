// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package null

import (
	"encoding/json"

	"github.com/trim21/go-bencode"
)

var _ json.Marshaler = (*Null[any])(nil)
var _ json.Unmarshaler = (*Null[any])(nil)
var _ bencode.Unmarshaler = (*Null[any])(nil)
var _ bencode.Marshaler = (*Null[any])(nil)
var _ bencode.IsZeroValue = (*Null[any])(nil)

// Null is a nullable type.
type Null[T any] struct {
	Value T
	Set   bool
}

func New[T any](t T) Null[T] {
	return Null[T]{
		Value: t,
		Set:   true,
	}
}

func NewFromPtr[T any](p *T) Null[T] {
	if p == nil {
		return Null[T]{}
	}

	return Null[T]{
		Value: *p,
		Set:   true,
	}
}

func (t Null[T]) Ptr() *T {
	if t.Set {
		return &t.Value
	}

	return nil
}

// Default return default value its value is Null or not Set.
func (t Null[T]) Default(v T) T {
	if t.Set {
		return t.Value
	}

	return v
}

func (t Null[T]) Interface() any {
	if t.Set {
		return t.Value
	}

	return nil
}

var nullBytes = []byte("null")

func (t Null[T]) MarshalJSON() ([]byte, error) {
	if !t.Set {
		return nullBytes, nil
	}

	return json.Marshal(t.Value)
}

// UnmarshalJSON implements json.Unmarshaler.
func (t *Null[T]) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}

	t.Set = true
	return json.Unmarshal(data, &t.Value)
}

func (t Null[T]) IsZeroBencodeValue() bool {
	return !t.Set
}

func (t Null[T]) MarshalBencode() ([]byte, error) {
	return bencode.Marshal(t.Value)
}

func (t *Null[T]) UnmarshalBencode(data []byte) error {
	t.Set = true
	return bencode.Unmarshal(data, &t.Value)
}
