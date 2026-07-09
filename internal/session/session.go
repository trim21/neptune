// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// session.go — per-session shared resources passed to all downloads.

package session

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
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
	"neptune/internal/mse"
	"neptune/internal/pkg/filepool"
	"neptune/internal/pkg/flowrate"
	"neptune/internal/pkg/global"
	"neptune/internal/pkg/random"
	"neptune/internal/pkg/ratelimit"
	"neptune/internal/pkg/unsafe"
	"neptune/internal/util"
)

// Session holds per-process shared resources: connection semaphore, rate
// limiters, file pool, DHT, and configuration constants that do not change
// after startup.
type Session struct {
	Ctx                context.Context
	MSESelector        mse.CryptoSelector
	DownloadLimiter    *ratelimit.Limiter
	DHT                *dht.DHT
	FilePool           *filepool.FilePool
	HTTP               *resty.Client
	ConnSem            *semaphore.Weighted
	IPv6               atomic.Pointer[netip.Addr]
	PieceUploadRate    *flowrate.Monitor
	UploadLimiter      *ratelimit.Limiter
	Cancel             context.CancelFunc
	IPv4               atomic.Pointer[netip.Addr]
	PieceDownloadRate  *flowrate.Monitor
	UploadQ            chan func()
	ResumePath         string
	TorrentPath        string
	randKey            []byte
	Config             config.Config
	RecheckOnComplete  atomic.Bool
	ConnCount          atomic.Uint32
	MSEPreferredCrypto mse.CryptoMethod
	MSEForce           bool
	MSEEnabled         bool
	Debug              bool
}

// New creates a Session for a neptune process.
func New(cfg config.Config, sessionPath string, debug bool) *Session {
	ctx, cancel := context.WithCancel(context.Background())

	var outgoingMseDisabled bool
	var mseSelector mse.CryptoSelector
	var msePreferredCrypto mse.CryptoMethod

	cryptoMode, err := config.ParseCryptoMode(cfg.App.Crypto)
	if err != nil {
		panic(fmt.Sprintf("invalid `application.crypto` config: %v", err))
	}

	switch cryptoMode {
	case config.CryptoForce:
		mseSelector = mse.ForceCrypto
		msePreferredCrypto = mse.AllSupportedCrypto
	case config.CryptoPrefer:
		mseSelector = mse.PreferCrypto
		msePreferredCrypto = mse.AllSupportedCrypto
	case config.CryptoPreferNoEncryption:
		mseSelector = mse.DefaultCryptoSelector
		msePreferredCrypto = mse.CryptoMethodPlaintext
	case config.CryptoNone:
		outgoingMseDisabled = true
		mseSelector = mse.DefaultCryptoSelector
		msePreferredCrypto = mse.CryptoMethodPlaintext
	}

	conn, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", fmt.Sprintf(":%d", cfg.App.P2PPort))
	if err != nil {
		log.Fatal().Err(err).Msg("failed to listen on dht")
	}
	_ = conn // reserved for DHT, currently disabled

	v4, v6, _ := util.GetIPAddress()

	s := &Session{
		Ctx:    ctx,
		Cancel: cancel,

		Config: cfg,

		DHT:      nil, // disabled for now
		FilePool: filepool.New(),
		HTTP: resty.NewWithClient(&http.Client{
			Transport: &http.Transport{
				DialContext:           conntrack.NewDialContextFunc(conntrack.DialWithName("announce"), conntrack.DialWithTracing()),
				DisableCompression:    true,
				MaxIdleConns:          cfg.App.MaxHTTPParallel,
				MaxIdleConnsPerHost:   2,
				IdleConnTimeout:       time.Minute,
				ResponseHeaderTimeout: time.Second * 30,
			},
			Timeout: time.Minute * 5,
		}).SetHeader("User-Agent", global.UserAgent).SetRedirectPolicy(resty.NoRedirectPolicy()),

		ConnSem: semaphore.NewWeighted(int64(cfg.App.GlobalConnectionLimit)),

		DownloadLimiter: ratelimit.New(cfg.App.GlobalDownloadSpeedLimit),
		UploadLimiter:   ratelimit.New(cfg.App.GlobalUploadSpeedLimit),

		PieceDownloadRate: flowrate.New(time.Second, 5*time.Second),
		PieceUploadRate:   flowrate.New(time.Second, 5*time.Second),

		MSEEnabled:         !outgoingMseDisabled,
		MSEForce:           cryptoMode == config.CryptoForce,
		MSESelector:        mseSelector,
		MSEPreferredCrypto: msePreferredCrypto,

		ResumePath:  filepath.Join(sessionPath, "resume"),
		TorrentPath: filepath.Join(sessionPath, "torrents"),

		randKey: random.Bytes(32),
		Debug:   debug,
	}

	s.RecheckOnComplete.Store(cfg.App.RecheckOnComplete)

	s.IPv4.Store(v4)
	s.IPv6.Store(v6)
	return s
}

func (s *Session) PeerPriority(peer netip.AddrPort) uint32 {
	if peer.Addr().Is4() {
		localV4 := s.IPv4.Load()
		if localV4 == nil {
			return bep40.SimplePriority(s.randKey, unsafe.Bytes(peer.String()))
		}
		return bep40.Priority4(netip.AddrPortFrom(*localV4, s.Config.App.P2PPort), peer)
	}

	if peer.Addr().Is6() {
		localV6 := s.IPv6.Load()
		if localV6 == nil {
			return bep40.SimplePriority(s.randKey, unsafe.Bytes(peer.String()))
		}
		return bep40.Priority6(netip.AddrPortFrom(*localV6, s.Config.App.P2PPort), peer)
	}

	panic(fmt.Sprintf("unexpected addrPort address format %+v", peer))
}

func (s *Session) Enqueue(fn func()) bool {
	select {
	case s.UploadQ <- fn:
		return true
	default:
		return false
	}
}

func (s *Session) InitMetrics() {
	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "neptune_client_connections",
		Help: "Current number connections tracked by client",
	}, func() float64 {
		return float64(s.ConnCount.Load())
	}))
}
