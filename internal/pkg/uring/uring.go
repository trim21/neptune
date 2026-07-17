// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only
//
// Vendored and simplified from github.com/godzie44/go-uring (MIT).
// Stripped to Read/Write operations and single-operation ring usage only.

//go:build linux

package uring

import (
	"os"
	"syscall"
	"unsafe"
)

// ---------------------------------------------------------------------------
// Syscall numbers and constants.
// ---------------------------------------------------------------------------

const (
	sysRingSetup = 425 // SYS_IO_URING_SETUP
	sysRingEnter = 426 // SYS_IO_URING_ENTER

	enterGetEvents = 1 << 0 // IORING_ENTER_GETEVENTS

	// mmap offsets.
	cqRingOffset = 0x8000000
	sqesOffset   = 0x10000000
)

// SQE flags.
const (
	sqeFixedFileFlag  = 1 << 0 // IOSQE_FIXED_FILE
	sqeIODrainFlag    = 1 << 1 // IOSQE_IO_DRAIN
	sqeIOLinkFlag     = 1 << 2 // IOSQE_IO_LINK
	sqeIOHardLinkFlag = 1 << 3 // IOSQE_IO_HARDLINK
	sqeAsyncFlag      = 1 << 4 // IOSQE_ASYNC
)

// ---------------------------------------------------------------------------
// SQEntry — matches struct io_uring_sqe (64 bytes).
// ---------------------------------------------------------------------------

type SQEntry struct {
	OpCode      uint8
	Flags       uint8
	IoPrio      uint16
	Fd          int32
	Off         uint64
	Addr        uint64
	Len         uint32
	OpcodeFlags uint32
	UserData    uint64

	BufIG       uint16
	Personality uint16
	SpliceFdIn  int32
	_pad2       [2]uint64
}

func (sqe *SQEntry) fill(op uint8, fd int32, addr uintptr, length uint32, offset uint64) {
	*sqe = SQEntry{}
	sqe.OpCode = op
	sqe.Fd = fd
	sqe.Off = offset
	sqe.Addr = uint64(addr)
	sqe.Len = length
}

// CQEvent — matches struct io_uring_cqe (16 bytes).
type CQEvent struct {
	UserData uint64
	Res      int32
	Flags    uint32
}

func (cqe *CQEvent) Error() error {
	if cqe.Res < 0 {
		return syscall.Errno(uintptr(-cqe.Res))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Ring parameters (kernel-facing).
// ---------------------------------------------------------------------------

type sqRingParams struct {
	head        uint32
	tail        uint32
	ringMask    uint32
	ringEntries uint32
	flags       uint32
	dropped     uint32
	array       uint32
	resv1       uint32
	resv2       uint64
}

type cqRingParams struct {
	head        uint32
	tail        uint32
	ringMsk     uint32
	ringEntries uint32
	overflow    uint32
	cqes        uint32
	flags       uint32
	resv1       uint32
	resv2       uint64
}

type ringParams struct {
	sqEntries    uint32
	cqEntries    uint32
	flags        uint32
	sqThreadCPU  uint32
	sqThreadIdle uint32
	features     uint32
	wqFD         uint32
	resv         [3]uint32
	sqOffset     sqRingParams
	cqOffset     cqRingParams
}

// ---------------------------------------------------------------------------
// SQ / CQ ring metadata.
// ---------------------------------------------------------------------------

type sqRing struct {
	buff         []byte
	sqeBuff      []byte
	kHead        *uint32
	kTail        *uint32
	kRingMask    *uint32
	kRingEntries *uint32
	kFlags       *uint32
	kDropped     *uint32
	kArray       *uint32
	sqeTail      uint32
	sqeHead      uint32
}

type cqRing struct {
	buff         []byte
	cqeBuff      *CQEvent
	kHead        *uint32
	kTail        *uint32
	kRingMask    *uint32
	kRingEntries *uint32
	kOverflow    *uint32
}

// ---------------------------------------------------------------------------
// Ring.
// ---------------------------------------------------------------------------

type Ring struct {
	params ringParams
	sq     sqRing
	cq     cqRing
	fd     int
}

// New creates an io_uring with the given number of SQ/CQ entries.
func New(entries uint32) (*Ring, error) {
	params := ringParams{sqEntries: entries, cqEntries: entries}
	fd, err := setup(entries, &params)
	if err != nil {
		return nil, err
	}
	r := &Ring{fd: fd, params: params}
	if err := r.allocRing(); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return r, nil
}

// Close closes the io_uring instance. Cancels all in-flight operations.
func (r *Ring) Close() error {
	return r.free()
}

// Fd returns the ring's file descriptor.
func (r *Ring) Fd() int { return r.fd }

// allocRing mmap-s the SQ, CQ, and SQE regions.
func (r *Ring) allocRing() error {
	p := &r.params

	r.sq.buff = nil
	r.cq.buff = nil

	// SQ ring mmap.
	sqRingSize := uint64(p.sqOffset.array) + uint64(p.sqEntries)*uint64(unsafe.Sizeof(uint32(0)))
	data, err := syscall.Mmap(r.fd, 0, int(sqRingSize), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED|syscall.MAP_POPULATE)
	if err != nil {
		return os.NewSyscallError("mmap sq_ring", err)
	}
	r.sq.buff = data

	ringStart := unsafe.Pointer(&data[0])
	r.sq.kHead = (*uint32)(unsafe.Add(ringStart, p.sqOffset.head))
	r.sq.kTail = (*uint32)(unsafe.Add(ringStart, p.sqOffset.tail))
	r.sq.kRingMask = (*uint32)(unsafe.Add(ringStart, p.sqOffset.ringMask))
	r.sq.kRingEntries = (*uint32)(unsafe.Add(ringStart, p.sqOffset.ringEntries))
	r.sq.kFlags = (*uint32)(unsafe.Add(ringStart, p.sqOffset.flags))
	r.sq.kDropped = (*uint32)(unsafe.Add(ringStart, p.sqOffset.dropped))
	r.sq.kArray = (*uint32)(unsafe.Add(ringStart, p.sqOffset.array))

	// CQ ring mmap.
	cqRingSize := uint64(p.cqOffset.cqes) + uint64(p.cqEntries)*uint64(unsafe.Sizeof(CQEvent{}))
	data, err = syscall.Mmap(r.fd, int64(cqRingOffset), int(cqRingSize), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED|syscall.MAP_POPULATE)
	if err != nil {
		r.free()
		return os.NewSyscallError("mmap cq_ring", err)
	}
	r.cq.buff = data

	ringStart = unsafe.Pointer(&data[0])
	r.cq.kHead = (*uint32)(unsafe.Add(ringStart, p.cqOffset.head))
	r.cq.kTail = (*uint32)(unsafe.Add(ringStart, p.cqOffset.tail))
	r.cq.kRingMask = (*uint32)(unsafe.Add(ringStart, p.cqOffset.ringMsk))
	r.cq.kRingEntries = (*uint32)(unsafe.Add(ringStart, p.cqOffset.ringEntries))
	r.cq.kOverflow = (*uint32)(unsafe.Add(ringStart, p.cqOffset.overflow))
	r.cq.cqeBuff = (*CQEvent)(unsafe.Add(ringStart, p.cqOffset.cqes))

	// SQE array mmap.
	sqesSize := uintptr(p.sqEntries) * unsafe.Sizeof(SQEntry{})
	b, err := syscall.Mmap(r.fd, int64(sqesOffset), int(sqesSize), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED|syscall.MAP_POPULATE)
	if err != nil {
		r.free()
		return os.NewSyscallError("mmap sqes", err)
	}
	r.sq.sqeBuff = b

	return nil
}

func (r *Ring) free() error {
	var errs [3]error
	errs[0] = syscall.Munmap(r.sq.buff)
	if r.cq.buff != nil && &r.cq.buff[0] != &r.sq.buff[0] {
		errs[1] = syscall.Munmap(r.cq.buff)
	}
	errs[2] = syscall.Close(r.fd)
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// SQE submission.
// ---------------------------------------------------------------------------

// NextSQE returns a pointer to the next available SQE or an error if the SQ is full.
func (r *Ring) NextSQE() (*SQEntry, error) {
	head := *r.sq.kHead
	next := r.sq.sqeTail + 1

	if next-head <= *r.sq.kRingEntries {
		idx := r.sq.sqeTail & *r.sq.kRingMask * uint32(unsafe.Sizeof(SQEntry{}))
		entry := (*SQEntry)(unsafe.Pointer(&r.sq.sqeBuff[idx]))
		r.sq.sqeTail = next
		return entry, nil
	}
	return nil, os.NewSyscallError("sq_ring", os.ErrExist)
}

// QueueSQE fills the next SQE from op and increments the SQ tail.
func (r *Ring) QueueSQE(op ReadWriteOp, flags uint8, userData uint64) error {
	sqe, err := r.NextSQE()
	if err != nil {
		return err
	}
	op.PrepSQE(sqe)
	sqe.Flags = flags
	sqe.UserData = userData
	return nil
}

// flushSQ writes SQE indices into the SQ array and advances the kernel SQ tail.
func (r *Ring) flushSQ() uint32 {
	mask := *r.sq.kRingMask
	tail := *r.sq.kTail
	subCnt := r.sq.sqeTail - r.sq.sqeHead

	if subCnt == 0 {
		return tail - *r.sq.kHead
	}

	for range subCnt {
		*(*uint32)(unsafe.Add(unsafe.Pointer(r.sq.kArray), tail&mask*uint32(unsafe.Sizeof(uint32(0))))) = r.sq.sqeHead & mask
		tail++
		r.sq.sqeHead++
	}

	*r.sq.kTail = tail
	return tail - *r.sq.kHead
}

// Submit submits queued SQEs to the kernel.
func (r *Ring) Submit() (uint, error) {
	flushed := r.flushSQ()
	consumed, err := enter(r.fd, flushed, 0, enterGetEvents)
	return consumed, err
}

// SubmitAndWait submits all queued SQEs and waits for a single CQE.
func (r *Ring) SubmitAndWait() (*CQEvent, error) {
	toSubmit := r.flushSQ()
	return r.getCQEvent(toSubmit)
}

// WaitCQE blocks until one CQE is available.
func (r *Ring) WaitCQE() (*CQEvent, error) {
	return r.getCQEvent(0)
}

// PeekCQE returns the first available CQE without blocking.
func (r *Ring) PeekCQE() (*CQEvent, error) {
	return r.peekCQEvent()
}

// SeenCQE advances the CQ ring by 1.
func (r *Ring) SeenCQE(_ *CQEvent) {
	r.AdvanceCQ(1)
}

// AdvanceCQ advances the CQ ring by n entries.
func (r *Ring) AdvanceCQ(n uint32) {
	*r.cq.kHead += n
}

// peekCQEvent returns the first available CQE or nil if none is ready.
func (r *Ring) peekCQEvent() (*CQEvent, error) {
	head := *r.cq.kHead
	tail := *r.cq.kTail
	mask := *r.cq.kRingMask

	if head == tail {
		return nil, os.NewSyscallError("cq_ring", os.ErrNotExist)
	}

	return (*CQEvent)(unsafe.Add(unsafe.Pointer(r.cq.cqeBuff), uintptr(head&mask)*unsafe.Sizeof(CQEvent{}))), nil
}

// getCQEvent blocks until a CQE is available, optionally submitting SQEs first.
func (r *Ring) getCQEvent(toSubmit uint32) (*CQEvent, error) {
	for {
		cqe, err := r.peekCQEvent()
		if err != nil {
			_, err2 := enter(r.fd, toSubmit, 1, enterGetEvents)
			if err2 != nil {
				return nil, err2
			}
			toSubmit = 0
			continue
		}
		return cqe, nil
	}
}

// ---------------------------------------------------------------------------
// Read / Write operations.
// ---------------------------------------------------------------------------

// opcodes from io_uring.h.
const (
	opNop         uint8 = 0  // IORING_OP_NOP
	opRead        uint8 = 22 // IORING_OP_READ
	opWrite       uint8 = 23 // IORING_OP_WRITE
	opAsyncCancel uint8 = 14 // IORING_OP_ASYNC_CANCEL
)

// ReadWriteOp is implemented by ReadOp, WriteOp, and CancelOp.
type ReadWriteOp interface {
	PrepSQE(sqe *SQEntry)
}

// Nop creates an operation that completes without performing I/O.
func Nop() ReadWriteOp { return nopOp{} }

type nopOp struct{}

func (nopOp) PrepSQE(sqe *SQEntry) {
	sqe.fill(opNop, -1, 0, 0, 0)
}

// ReadOp is an io_uring IORING_OP_READ operation (equivalent to pread).
type ReadOp struct {
	buff []byte
	off  uint64
	fd   uintptr
}

// Read creates a ReadOp.
func Read(fd uintptr, buff []byte, offset uint64) *ReadOp {
	return &ReadOp{fd: fd, buff: buff, off: offset}
}

// PrepSQE fills the SQE for a read.
func (op *ReadOp) PrepSQE(sqe *SQEntry) {
	sqe.fill(opRead, int32(op.fd), uintptr(unsafe.Pointer(&op.buff[0])), uint32(len(op.buff)), op.off)
}

// WriteOp is an io_uring IORING_OP_WRITE operation (equivalent to pwrite).
type WriteOp struct {
	buff []byte
	off  uint64
	fd   uintptr
}

// Write creates a WriteOp.
func Write(fd uintptr, buff []byte, offset uint64) *WriteOp {
	return &WriteOp{fd: fd, buff: buff, off: offset}
}

// PrepSQE fills the SQE for a write.
func (op *WriteOp) PrepSQE(sqe *SQEntry) {
	sqe.fill(opWrite, int32(op.fd), uintptr(unsafe.Pointer(&op.buff[0])), uint32(len(op.buff)), op.off)
}

// CancelOp is an io_uring IORING_OP_ASYNC_CANCEL operation.
// Target is the userData of the SQE to cancel.
type CancelOp struct {
	Target uint64
}

// PrepSQE fills the SQE for an async cancel.
func (op *CancelOp) PrepSQE(sqe *SQEntry) {
	sqe.fill(opAsyncCancel, -1, uintptr(op.Target), 0, 0)
}

// ---------------------------------------------------------------------------
// Syscall helpers.
// ---------------------------------------------------------------------------

func setup(entries uint32, p *ringParams) (int, error) {
	fd, _, errno := syscall.Syscall(sysRingSetup, uintptr(entries), uintptr(unsafe.Pointer(p)), 0)
	if errno != 0 {
		return int(fd), os.NewSyscallError("io_uring_setup", errno)
	}
	return int(fd), nil
}

func enter(ringFD int, toSubmit uint32, minComplete uint32, flags uint32) (uint, error) {
	consumed, _, errno := syscall.Syscall6(sysRingEnter, uintptr(ringFD), uintptr(toSubmit), uintptr(minComplete), uintptr(flags), 0, 0)
	if errno != 0 {
		return 0, os.NewSyscallError("io_uring_enter", errno)
	}
	return uint(consumed), nil
}
