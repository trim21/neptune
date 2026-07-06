// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
	"github.com/trim21/conntrack"
	"go.uber.org/atomic"
	"golang.org/x/sync/semaphore"

	"neptune/internal/bep40"
	"neptune/internal/config"
	"neptune/internal/dht"
	"neptune/internal/metainfo"
	"neptune/internal/pkg/filepool"
	"neptune/internal/pkg/flowrate"
	"neptune/internal/pkg/global"
	"neptune/internal/pkg/random"
	"neptune/internal/pkg/ratelimit"
	"neptune/internal/pkg/unsafe"
	"neptune/internal/util"
)

func New(cfg config.Config, sessionPath string, debug bool) *Client {
	ctx, cancel := context.WithCancel(context.Background())

	// var mseDisabled bool
	// var mseSelector mse.CryptoSelector
	// switch cfg.App.Crypto {
	// case "force":
	//	mseSelector = mse.ForceCrypto
	// case "prefer":
	//	mseSelector = mse.PreferCrypto
	//case "prefer-not":
	//	mseSelector = mse.DefaultCryptoSelector
	//case "", "disable":
	//	mseDisabled = true
	//default:
	//	panic(fmt.Sprintf("invalid `application.crypto` config %q,
	//	only 'prefer'(default) 'prefer-not', 'disable' or 'force' are allowed", cfg.App.Crypto))
	//}

	p2pPort, p2pListener, err := parseAndResolvePort(cfg.App.P2PPort)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to resolve p2p port")
	}

	conn, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", fmt.Sprintf(":%d", p2pPort))
	if err != nil {
		log.Fatal().Err(err).Msg("failed to listen on dht")
	}

	d := dht.Start(conn, p2pPort)

	v4, v6, _ := util.GetIPAddress()

	c := &Client{
		Config:      cfg,
		p2pPort:     p2pPort,
		p2pListener: p2pListener,
		ctx:         ctx,
		cancel:      cancel,
		sem:         semaphore.NewWeighted(int64(cfg.App.GlobalConnectionLimit)),
		checkQueue:  make([]metainfo.Hash, 0, 3),
		downloadMap: make(map[metainfo.Hash]*Download),
		connChan:    make(chan incomingConn, 1),

		dht: d,

		filePool: filepool.New(),

		pieceUploadRate:   flowrate.New(time.Second, 5*time.Second),
		pieceDownloadRate: flowrate.New(time.Second, 5*time.Second),

		downloadLimiter: ratelimit.New(cfg.App.GlobalDownloadSpeedLimit),
		uploadLimiter:   ratelimit.New(cfg.App.GlobalUploadSpeedLimit),

		http: resty.NewWithClient(&http.Client{
			Transport: &http.Transport{
				DialContext:           conntrack.NewDialContextFunc(conntrack.DialWithName("announce"), conntrack.DialWithTracing()),
				DisableCompression:    true, // normally gzipped bencode is larger than original content
				MaxIdleConns:          cfg.App.MaxHTTPParallel,
				MaxIdleConnsPerHost:   2,
				IdleConnTimeout:       time.Minute,
				ResponseHeaderTimeout: time.Second * 30,
			},
			Timeout: time.Minute * 5,
		}).SetHeader("User-Agent", global.UserAgent).SetRedirectPolicy(resty.NoRedirectPolicy()),

		resumePath:  filepath.Join(sessionPath, "resume"),
		torrentPath: filepath.Join(sessionPath, "torrents"),

		fh:      make(map[string]*os.File),
		randKey: random.Bytes(32),
		ipv4:    *atomic.NewPointer(v4),
		ipv6:    *atomic.NewPointer(v6),
		debug:   debug,
	}

	c.startUploadPool()
	c.initMetrics()

	return c
}

func (c *Client) initMetrics() {
	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "neptune_client_connections",
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
	ctx               context.Context
	p2pListener       net.Listener
	filePool          *filepool.FilePool
	downloadLimiter   *ratelimit.Limiter
	connChan          chan incomingConn
	sem               *semaphore.Weighted
	uploadQ           chan uploadTask
	fh                map[string]*os.File
	pieceDownloadRate *flowrate.Monitor
	pieceUploadRate   *flowrate.Monitor
	dht               *dht.DHT
	ipv4              atomic.Pointer[netip.Addr]
	http              *resty.Client
	cancel            context.CancelFunc
	downloadMap       map[metainfo.Hash]*Download
	uploadLimiter     *ratelimit.Limiter
	ipv6              atomic.Pointer[netip.Addr]
	resumePath        string
	torrentPath       string
	randKey           []byte
	infoHashes        []metainfo.Hash
	downloads         []*Download
	checkQueue        []metainfo.Hash
	Config            config.Config
	connectionCount   atomic.Uint32
	m                 sync.RWMutex
	p2pPort           uint16
	debug             bool
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

	return DownloadInfo{
		Name:   d.info.Name,
		Tags:   d.tags,
		Custom: d.custom,
	}, nil
}

func (c *Client) PeerPriority(peer netip.AddrPort) uint32 {
	if peer.Addr().Is4() {
		localV4 := c.ipv4.Load()
		if localV4 == nil {
			return bep40.SimplePriority(c.randKey, unsafe.Bytes(peer.String()))
		}

		return bep40.Priority4(netip.AddrPortFrom(*localV4, c.p2pPort), peer)
	}

	if peer.Addr().Is6() {
		localV6 := c.ipv6.Load()
		if localV6 == nil {
			return bep40.SimplePriority(c.randKey, unsafe.Bytes(peer.String()))
		}

		return bep40.Priority6(netip.AddrPortFrom(*localV6, c.p2pPort), peer)
	}

	panic(fmt.Sprintf("unexpected addrPort address format %+v", peer))
}

// parseAndResolvePort parses a P2PPort config value that can be a single port
// ("50047") or a range ("50047-50100"). It binds a TCP listener and returns
// both the port and the open listener — no TOCTOU race with a later bind.
// When a range is given, ports are shuffled randomly and the first available
// one wins.
func parseAndResolvePort(s string) (uint16, net.Listener, error) {
	start, end, err := config.ValidateP2PPort(s)
	if err != nil {
		return 0, nil, err
	}

	if start == end {
		l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", fmt.Sprintf(":%d", start))
		if err != nil {
			return 0, nil, fmt.Errorf("failed to bind port %d: %w", start, err)
		}
		return start, l, nil
	}

	// Randomly shuffle ports in the range and try to bind TCP.
	n := int(end - start + 1)
	ports := make([]uint16, n)
	for i := range n {
		ports[i] = start + uint16(i)
	}
	rand.Shuffle(len(ports), func(i, j int) { ports[i], ports[j] = ports[j], ports[i] })

	var lastErr error
	for _, port := range ports {
		l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			return port, l, nil
		}
		lastErr = err
	}

	return 0, nil, fmt.Errorf("no available port in range %d-%d: %w", start, end, lastErr)
}
