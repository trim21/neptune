// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package global

import (
	"context"
	"net"
	"time"

	"github.com/trim21/conntrack"
)

const ConnTimeout = time.Minute

func Init(debug bool) {
	if debug {
		dialTracked = conntrack.NewDialContextFunc(
			conntrack.DialWithTracing(),
			conntrack.DialWithName("p2p"),
			conntrack.DialWithDialer(&net.Dialer{Timeout: time.Minute}),
		)
	} else {
		dialTracked = conntrack.NewDialContextFunc(
			conntrack.DialWithName("p2p"),
			conntrack.DialWithDialer(&net.Dialer{Timeout: time.Minute}),
		)
	}
}

var dialTracked func(context.Context, string, string) (net.Conn, error)

// Dial will try to establish a connection.
func Dial(ctx context.Context, network, address string) (net.Conn, error) {
	return dialTracked(ctx, network, address)
}
