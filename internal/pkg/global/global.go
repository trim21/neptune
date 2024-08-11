// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package global

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/trim21/conntrack"

	"neptune/internal/version"
)

var PeerIDPrefix = fmt.Sprintf("-NE%x%x%x0-", version.MAJOR, version.MINOR, version.PATCH)

const ConnTimeout = time.Minute

var dialTracked = conntrack.NewDialContextFunc(
	conntrack.DialWithTracing(),
	conntrack.DialWithName("bt"),
	conntrack.DialWithDialer(&net.Dialer{Timeout: time.Minute}),
)

// Dial will try to establish a connection, should be used as peer 2 peer connections.
func Dial(ctx context.Context, network, address string) (net.Conn, error) {
	return dialTracked(ctx, network, address)
}
