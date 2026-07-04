// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package res

import (
	"net/http"

	"github.com/bytedance/sonic"

	"neptune/internal/pkg/unsafe"
)

func JSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	enc := sonic.ConfigDefault.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(value)
}

func Text(w http.ResponseWriter, code int, value string) {
	w.Header().Set("Content-Type", "plain/text")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(unsafe.Bytes(value))
}
