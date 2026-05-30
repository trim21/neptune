// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package web

import "errors"

func CodeError(code int, err error) error {
	if err == nil {
		err = errors.New("unknown error")
	}
	return resError{error: err, code: code}
}

type resError struct {
	error
	code int
}

func (r resError) AppErrCode() int {
	return r.code
}
