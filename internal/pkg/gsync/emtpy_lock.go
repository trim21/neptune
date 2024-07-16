// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package gsync

import (
	"sync"
)

type EmptyLock struct{}

func (e EmptyLock) Lock()   {}
func (e EmptyLock) Unlock() {}

var _ sync.Locker = (*EmptyLock)(nil)
