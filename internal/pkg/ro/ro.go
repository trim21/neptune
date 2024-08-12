// Package mem provides the mem.RO type that allows you to cheaply pass &
// access either a read-only []byte or a string.

// https://github.com/go4org/mem
// Copyright 2020 The Go4 AUTHORS
// SPDX-License-Identifier: Apache-2.0

package ro

import (
	"bytes"
	"io"
	"unsafe"
)

// RO is a read-only view of some bytes of memory. It may be backed
// by a string or []byte. Notably, unlike a string, the memory is not
// guaranteed to be immutable. While the length is fixed, the
// underlying bytes might change if interleaved with code that's
// modifying the underlying memory.
//
// RO is a value type that's the same size of a Go string. Its various
// methods should inline & compile to the equivalent operations
// working on a string or []byte directly.
//
// Unlike a Go string, RO is not 'comparable' (it can't be a map key
// or support ==). Use its Equal method to compare. This is done so an
// RO backed by a later-mutating []byte doesn't break invariants in
// Go's map implementation.
type RO struct {
	m string
}

func S(s string) RO { return RO{m: s} }

func B(b []byte) RO {
	return RO{m: string(b)}
}

func (r RO) str() string { return r.m }

func (r RO) bytes() []byte {
	s := r.str()
	d := unsafe.StringData(s)
	return unsafe.Slice(d, len(s))
}

// Len returns len(r).
func (r RO) Len() int { return len(r.m) }

// At returns r[i].
func (r RO) At(i int) byte { return r.m[i] }

// Copy copies up to len(dest) bytes into dest from r and returns the
// number of bytes copied, the min(r.Len(), len(dest)).
func (r RO) Copy(dest []byte) int { return copy(dest, r.m) }

// EqualString reports whether r and s are the same length and contain
// the same bytes.
func (r RO) EqualString(s string) bool { return r.str() == s }

// EqualBytes reports whether r and b are the same length and contain
// the same bytes.
func (r RO) EqualBytes(b []byte) bool { return bytes.Equal(r.bytes(), b) }

// Less reports whether r < r2.
func (r RO) Less(r2 RO) bool { return r.str() < r2.str() }

func (r RO) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(r.bytes())
	return int64(n), err
}

// StringCopy returns m's contents in a newly allocated string.
func (r RO) StringCopy() string {
	return string(r.bytes())
}
