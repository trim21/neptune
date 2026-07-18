// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package diskio

import (
	"context"
	"os"
)

type operationInfo struct {
	name  string
	bytes int64
}

// Operation is a structured positional file IO operation.
type Operation interface {
	operationInfo() operationInfo
}

// PRead describes one positional read from an open file.
type PRead struct {
	File   *os.File
	Buffer []byte
	Offset int64
}

func (op PRead) operationInfo() operationInfo {
	return operationInfo{name: "read", bytes: int64(len(op.Buffer))}
}

// PWrite describes one positional write to an open file.
type PWrite struct {
	File   *os.File
	Buffer []byte
	Offset int64
}

func (op PWrite) operationInfo() operationInfo {
	return operationInfo{name: "write", bytes: int64(len(op.Buffer))}
}

// Result is the completion of one structured IO operation.
type Result struct {
	Err error
	N   int
}

// Executor runs structured operations after a device queue dispatches them.
type Executor interface {
	Execute(context.Context, Operation) Result
}
