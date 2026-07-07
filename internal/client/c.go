// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package client

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"

	"neptune/internal/config"
	"neptune/internal/metainfo"
	"neptune/internal/pkg/flowrate"
	"neptune/internal/pkg/ratelimit"
	"neptune/internal/session"
)

func New(cfg config.Config, sessionPath string, debug bool) *Client {
	sess := session.New(cfg, sessionPath, debug)

	c := &Client{
		session:     sess,
		checkQueue:  make([]metainfo.Hash, 0, 3),
		downloadMap: make(map[metainfo.Hash]*Download),
		connChan:    make(chan incomingConn, 1),
		fh:          make(map[string]*os.File),
	}

	c.startUploadPool()
	sess.InitMetrics()

	return c
}

type incomingConn struct {
	conn      net.Conn
	addr      netip.AddrPort
	encrypted bool
}

type Client struct {
	session     *session.Session
	downloadMap map[metainfo.Hash]*Download
	connChan    chan incomingConn
	fh          map[string]*os.File
	downloads   []*Download
	infoHashes  []metainfo.Hash
	checkQueue  []metainfo.Hash
	m           sync.RWMutex
}

type DownloadInfo struct {
	Custom map[string]string
	Name   string
	Tags   []string
}

func (c *Client) GetTorrent(h metainfo.Hash) (DownloadInfo, error) {
	c.m.RLock()
	defer c.m.RUnlock()

	d, ok := c.downloadMap[h]
	if !ok {
		return DownloadInfo{}, fmt.Errorf("torrent %s not exists", h)
	}

	info := d.Info(nil)
	return DownloadInfo{
		Name:   info.Name,
		Tags:   info.Tags,
		Custom: info.Custom,
	}, nil
}

// Config returns the application configuration.
func (c *Client) Config() config.Config {
	return c.session.Config
}

// PieceDownloadRate is an adapter for JSON-RPC API that accesses the session rate.
func (c *Client) PieceDownloadRate() *flowrate.Monitor {
	return c.session.PieceDownloadRate
}

// PieceUploadRate is an adapter for JSON-RPC API that accesses the session rate.
func (c *Client) PieceUploadRate() *flowrate.Monitor {
	return c.session.PieceUploadRate
}

// DownloadLimiter is an adapter for JSON-RPC API.
func (c *Client) DownloadLimiter() *ratelimit.Limiter {
	return c.session.DownloadLimiter
}

// UploadLimiter is an adapter for JSON-RPC API.
func (c *Client) UploadLimiter() *ratelimit.Limiter {
	return c.session.UploadLimiter
}
