// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux

package uring

import (
	"os"
	"testing"
	"unsafe"

	"neptune/internal/pkg/sys"
)

// skipIfDisabled skips the test if NEPTUNE_TEST_URING=0 or kernel < 5.1.
func skipIfDisabled(t *testing.T) {
	t.Helper()
	if os.Getenv("NEPTUNE_TEST_URING") == "0" {
		t.Skip("io_uring tests disabled (NEPTUNE_TEST_URING=0)")
	}
	major, minor := sys.KernelVersion()
	if major < 5 || (major == 5 && minor < 1) {
		t.Skipf("io_uring requires kernel >= 5.1, got %d.%d", major, minor)
	}
}

func tmpFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp("", "uring_test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f
}

// ---------------------------------------------------------------------------
// Ring lifecycle
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	skipIfDisabled(t)

	r, err := New(8)
	if err != nil {
		t.Fatal(err)
	}
	if r.Fd() < 0 {
		t.Error("expected valid fd")
	}
	if err := r.Close(); err != nil {
		t.Error(err)
	}
}

func TestNewMinEntries(t *testing.T) {
	skipIfDisabled(t)

	r, err := New(1)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
}

func TestNewLargeEntries(t *testing.T) {
	skipIfDisabled(t)

	r, err := New(256)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
}

// ---------------------------------------------------------------------------
// Read operations
// ---------------------------------------------------------------------------

func TestRead(t *testing.T) {
	skipIfDisabled(t)

	f := tmpFile(t)
	content := []byte("hello io_uring read test\n")
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}

	r, err := New(2)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	buf := make([]byte, len(content))
	op := Read(f.Fd(), buf, 0)
	if err := r.QueueSQE(op, 0, 1); err != nil {
		t.Fatal(err)
	}

	cqe, err := r.SubmitAndWait()
	if err != nil {
		t.Fatal(err)
	}
	if cqe.Error() != nil {
		t.Fatal(cqe.Error())
	}
	if cqe.Res != int32(len(content)) {
		t.Fatalf("read %d bytes, expected %d", cqe.Res, len(content))
	}

	if string(buf) != string(content) {
		t.Fatalf("read %q, expected %q", buf, content)
	}

	r.SeenCQE(cqe)
}

func TestReadAtOffset(t *testing.T) {
	skipIfDisabled(t)

	f := tmpFile(t)
	content := []byte("0123456789ABCDEF")
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}

	r, err := New(2)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	buf := make([]byte, 6)
	if err := r.QueueSQE(Read(f.Fd(), buf, 10), 0, 1); err != nil {
		t.Fatal(err)
	}

	cqe, err := r.SubmitAndWait()
	if err != nil {
		t.Fatal(err)
	}
	if cqe.Error() != nil {
		t.Fatal(cqe.Error())
	}
	if string(buf) != "ABCDEF" {
		t.Fatalf("read %q, expected ABCDEF", buf)
	}
	r.SeenCQE(cqe)
}

// ---------------------------------------------------------------------------
// Write operations
// ---------------------------------------------------------------------------

func TestWrite(t *testing.T) {
	skipIfDisabled(t)

	f := tmpFile(t)
	r, err := New(2)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	data := []byte("uring write test data\n")
	if err := r.QueueSQE(Write(f.Fd(), data, 0), 0, 1); err != nil {
		t.Fatal(err)
	}

	cqe, err := r.SubmitAndWait()
	if err != nil {
		t.Fatal(err)
	}
	if cqe.Error() != nil {
		t.Fatal(cqe.Error())
	}
	if cqe.Res != int32(len(data)) {
		t.Fatalf("wrote %d bytes, expected %d", cqe.Res, len(data))
	}
	r.SeenCQE(cqe)

	// Verify on-disk content.
	readBack, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(readBack) != string(data) {
		t.Fatalf("on-disk: %q, expected %q", readBack, data)
	}
}

func TestWriteAtOffset(t *testing.T) {
	skipIfDisabled(t)

	f := tmpFile(t)
	// Pre-fill with zeros, then write at offset.
	if _, err := f.Write(make([]byte, 16)); err != nil {
		t.Fatal(err)
	}

	r, err := New(2)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	data := []byte("hello")
	if err := r.QueueSQE(Write(f.Fd(), data, 5), 0, 1); err != nil {
		t.Fatal(err)
	}

	cqe, err := r.SubmitAndWait()
	if err != nil {
		t.Fatal(err)
	}
	if cqe.Error() != nil {
		t.Fatal(cqe.Error())
	}
	r.SeenCQE(cqe)

	readBack, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	expected := make([]byte, 16)
	copy(expected[5:], data)
	if string(readBack) != string(expected) {
		t.Fatalf("on-disk: %q, expected %q", readBack, expected)
	}
}

// ---------------------------------------------------------------------------
// Multiple operations
// ---------------------------------------------------------------------------

func TestMultipleSubmit(t *testing.T) {
	skipIfDisabled(t)

	f := tmpFile(t)
	if _, err := f.Write([]byte("AAAAAAAAAAAAAAAAAAAA")); err != nil {
		t.Fatal(err)
	}

	r, err := New(8)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Queue 3 reads at different offsets.
	for i := byte(0); i < 3; i++ {
		buf := make([]byte, 1)
		op := Read(f.Fd(), buf, uint64(i*5))
		if err := r.QueueSQE(op, 0, uint64(i)); err != nil {
			t.Fatal(err)
		}
	}

	// Submit all queued SQEs, then wait for each CQE.
	if _, err := r.Submit(); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		cqe, err := r.WaitCQE()
		if err != nil {
			t.Fatal(err)
		}
		if cqe.Error() != nil {
			t.Fatal(cqe.Error())
		}
		if cqe.Res != 1 {
			t.Errorf("op %d: read %d bytes, expected 1", i, cqe.Res)
		}
		r.SeenCQE(cqe)
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestDoubleClose(t *testing.T) {
	skipIfDisabled(t)

	r, err := New(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Error(err)
	}
	// Second close is harmless (mmap'd regions already freed, fd already closed).
	// Should not panic.
	r.Close()
}

// ---------------------------------------------------------------------------
// CancelOp
// ---------------------------------------------------------------------------

func TestCancelOpPrepSQE(t *testing.T) {
	op := &CancelOp{Target: 42}
	var sqe SQEntry
	op.PrepSQE(&sqe)

	if sqe.OpCode != opAsyncCancel {
		t.Errorf("OpCode = %d, expected %d", sqe.OpCode, opAsyncCancel)
	}
	if sqe.Fd != -1 {
		t.Errorf("Fd = %d, expected -1", sqe.Fd)
	}
	if sqe.Addr != 42 {
		t.Errorf("Addr = %d, expected 42", sqe.Addr)
	}
}

// ---------------------------------------------------------------------------
// ReadWriteOp interface satisfaction
// ---------------------------------------------------------------------------

func TestOpsSatisfyInterface(t *testing.T) {
	var _ ReadWriteOp = &ReadOp{}
	var _ ReadWriteOp = &WriteOp{}
	var _ ReadWriteOp = &CancelOp{}
}

// ---------------------------------------------------------------------------
// SQEntry size
// ---------------------------------------------------------------------------

func TestSQEntrySize(t *testing.T) {
	// SQEntry must be exactly 64 bytes to match the kernel ABI.
	const expected = 64
	if size := unsafe.Sizeof(SQEntry{}); size != expected {
		t.Fatalf("SQEntry size = %d, expected %d", size, expected)
	}
}

// ---------------------------------------------------------------------------
// Integration: write then read via separate rings
// ---------------------------------------------------------------------------

func TestWriteThenRead(t *testing.T) {
	skipIfDisabled(t)

	f := tmpFile(t)
	data := []byte("integration test data\n")

	wr, err := New(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := wr.QueueSQE(Write(f.Fd(), data, 0), 0, 1); err != nil {
		t.Fatal(err)
	}
	cqe, err := wr.SubmitAndWait()
	if err != nil {
		t.Fatal(err)
	}
	if cqe.Error() != nil {
		t.Fatal(cqe.Error())
	}
	wr.SeenCQE(cqe)
	wr.Close()

	rr, err := New(2)
	if err != nil {
		t.Fatal(err)
	}
	defer rr.Close()

	buf := make([]byte, len(data))
	if err := rr.QueueSQE(Read(f.Fd(), buf, 0), 0, 1); err != nil {
		t.Fatal(err)
	}
	cqe, err = rr.SubmitAndWait()
	if err != nil {
		t.Fatal(err)
	}
	if cqe.Error() != nil {
		t.Fatal(cqe.Error())
	}
	if string(buf) != string(data) {
		t.Fatalf("read %q, expected %q", buf, data)
	}
	rr.SeenCQE(cqe)
}
