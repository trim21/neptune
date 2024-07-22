// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/trim21/conntrack"
	"go.uber.org/atomic"
	"golang.org/x/sync/semaphore"

	"neptune/internal/bep40"
	"neptune/internal/config"
	"neptune/internal/metainfo"
	"neptune/internal/pkg/flowrate"
	"neptune/internal/pkg/global"
	"neptune/internal/pkg/random"
	"neptune/internal/pkg/unsafe"
	"neptune/internal/util"
)

func New(cfg config.Config, sessionPath string, debug bool) *Client {
	ctx, cancel := context.WithCancel(context.Background())

	//var mseDisabled bool
	//var mseSelector mse.CryptoSelector
	//switch cfg.App.Crypto {
	//case "force":
	//	mseSelector = mse.ForceCrypto
	//case "prefer":
	//	mseSelector = mse.PreferCrypto
	//case "prefer-not":
	//	mseSelector = mse.DefaultCryptoSelector
	//case "", "disable":
	//	mseDisabled = true
	//default:
	//	panic(fmt.Sprintf("invalid `application.crypto` config %q, only 'prefer'(default) 'prefer-not', 'disable' or 'force' are allowed", cfg.App.Crypto))
	//}

	v4, v6, _ := util.GetIpAddress()

	c := &Client{
		Config:      cfg,
		ctx:         ctx,
		cancel:      cancel,
		sem:         semaphore.NewWeighted(int64(cfg.App.GlobalConnectionLimit)),
		checkQueue:  make([]metainfo.Hash, 0, 3),
		downloadMap: make(map[metainfo.Hash]*Download),
		connChan:    make(chan incomingConn, 1),

		ioUp:   flowrate.New(time.Second, time.Second),
		ioDown: flowrate.New(time.Second, time.Second),

		http: resty.NewWithClient(&http.Client{Transport: &http.Transport{
			MaxIdleConns:       cfg.App.MaxHTTPParallel,
			IdleConnTimeout:    30 * time.Second,
			DisableCompression: true,
			DialContext:        conntrack.NewDialContextFunc(conntrack.DialWithName("http")),
		}}).SetHeader("User-Agent", global.UserAgent).SetRedirectPolicy(resty.NoRedirectPolicy()),

		//mseDisabled: mseDisabled,
		//mseSelector: mseSelector,
		sessionPath: sessionPath,
		fh:          make(map[string]*os.File),
		randKey:     random.Bytes(32),
		ipv4:        *atomic.NewPointer(v4),
		ipv6:        *atomic.NewPointer(v6),
		debug:       debug,
	}

	c.initMetrics()

	return c
}

func (c *Client) initMetrics() {
	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "neptune_client_connections_count",
		Help: "Current number connections tracked by client",
	}, func() float64 {
		return float64(c.connectionCount.Load())
	}))
}

type incomingConn struct {
	conn net.Conn
	addr netip.AddrPort
}

type Client struct {
	ctx         context.Context
	http        *resty.Client
	cancel      context.CancelFunc
	downloadMap map[metainfo.Hash]*Download
	connChan    chan incomingConn
	sem         *semaphore.Weighted
	fh          map[string]*os.File

	ioDown *flowrate.Monitor
	ioUp   *flowrate.Monitor

	ipv4 atomic.Pointer[netip.Addr]
	ipv6 atomic.Pointer[netip.Addr]

	sessionPath string
	infoHashes  []metainfo.Hash
	downloads   []*Download
	checkQueue  []metainfo.Hash

	randKey []byte

	Config          config.Config
	connectionCount atomic.Uint32
	m               sync.RWMutex
	debug           bool
}

type DownloadInfo struct {
	Name string
	Tags []string
}

func (c *Client) GetTorrent(h metainfo.Hash) (DownloadInfo, error) {
	c.m.RLock()
	defer c.m.RUnlock()

	d, ok := c.downloadMap[h]
	if !ok {
		return DownloadInfo{}, fmt.Errorf("torrent %s not exists", h)
	}

	return DownloadInfo{
		Name: d.info.Name,
		Tags: d.tags,
	}, nil
}

func (c *Client) PeerPriority(peer netip.AddrPort) uint32 {
	if peer.Addr().Is4() {
		localV4 := c.ipv4.Load()
		if localV4 == nil {
			return bep40.SimplePriority(c.randKey, unsafe.Bytes(peer.String()))
		}

		return bep40.Priority4(netip.AddrPortFrom(*localV4, c.Config.App.P2PPort), peer)
	}

	if peer.Addr().Is6() {
		localV6 := c.ipv6.Load()
		if localV6 == nil {
			return bep40.SimplePriority(c.randKey, unsafe.Bytes(peer.String()))
		}

		return bep40.Priority6(netip.AddrPortFrom(*localV6, c.Config.App.P2PPort), peer)
	}

	panic(fmt.Sprintf("unexpected addrPort address format %+v", peer))
}
