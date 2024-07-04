// Copyright 2024 trim21 <trim21.me@gmail.com>
// Copyright 2021 Viacheslav Poturaev
// SPDX-License-Identifier: MIT
// https://github.com/swaggest/jsonrpc/blob/master/LICENSE

package jsonrpc

import (
	"sort"
)

// ErrWithAppCode exposes application error code.
type ErrWithAppCode interface {
	error
	AppErrCode() ErrorCode
}

// ValidationErrors is a list of validation errors.
//
// Key is field position (e.g. "path:id" or "body"), value is a list of issues with the field.
type ValidationErrors map[string][]string

// Error returns error message.
func (re ValidationErrors) Error() string {
	return "validation failed"
}

// Fields returns request errors by field location and name.
func (re ValidationErrors) Fields() map[string]any {
	res := make(map[string]any, len(re))

	for k, v := range re {
		sort.Strings(v)
		res[k] = v
	}

	return res
}
