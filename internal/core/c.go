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

	"github.com/anacrolix/torrent/mse"
	"github.com/go-resty/resty/v2"
	"github.com/jellydator/ttlcache/v3"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
	"github.com/samber/lo"
	"go.uber.org/atomic"
	"golang.org/x/sync/semaphore"

	"tyr/internal/bep40"
	"tyr/internal/config"
	"tyr/internal/meta"
	"tyr/internal/metainfo"
	imse "tyr/internal/mse"
	"tyr/internal/pkg/global"
	"tyr/internal/pkg/global/tasks"
	"tyr/internal/pkg/gslice"
	"tyr/internal/pkg/random"
	"tyr/internal/pkg/unsafe"
	"tyr/internal/util"
)

func New(cfg config.Config, sessionPath string) *Client {
	tr := &http.Transport{
		MaxIdleConns:       cfg.App.MaxHTTPParallel,
		IdleConnTimeout:    30 * time.Second,
		DisableCompression: true,
	}

	hc := &http.Client{Transport: tr}

	ctx, cancel := context.WithCancel(context.Background())

	var mseDisabled bool
	var mseSelector mse.CryptoSelector
	switch cfg.App.Crypto {
	case "force":
		mseSelector = imse.ForceCrypto
	case "", "prefer":
		mseSelector = imse.PreferCrypto
	case "prefer-not":
		mseSelector = mse.DefaultCryptoSelector
	case "disable":
		mseDisabled = true
	default:
		panic(fmt.Sprintf("invalid `application.crypto` config %q, only 'prefer'(default) 'prefer-not', 'disable' or 'force' are allowed", cfg.App.Crypto))
	}

	v4, v6, _ := util.GetIpAddress()

	return &Client{
		Config:      cfg,
		ctx:         ctx,
		cancel:      cancel,
		ch:          ttlcache.New[netip.AddrPort, connHistory](),
		sem:         semaphore.NewWeighted(int64(cfg.App.GlobalConnectionLimit)),
		checkQueue:  make([]metainfo.Hash, 0, 3),
		downloadMap: make(map[metainfo.Hash]*Download),
		connChan:    make(chan incomingConn, 1),
		http:        resty.NewWithClient(hc).SetHeader("User-Agent", global.UserAgent).SetRedirectPolicy(resty.NoRedirectPolicy()),
		mseDisabled: mseDisabled,
		mseSelector: mseSelector,
		sessionPath: sessionPath,
		fh:          make(map[string]*os.File),
		randKey:     random.Bytes(32),
		v4Addr:      *atomic.NewPointer(v4),
		v6Addr:      *atomic.NewPointer(v6),
	}
}

func (c *Client) initMetrics() {
	prometheus.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "tyr_client_connections_count",
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
	mseKeys     mse.SecretKeyIter
	connChan    chan incomingConn
	sem         *semaphore.Weighted
	mseSelector mse.CryptoSelector
	ch          *ttlcache.Cache[netip.AddrPort, connHistory]
	fh          map[string]*os.File
	v4Addr      atomic.Pointer[netip.Addr]
	v6Addr      atomic.Pointer[netip.Addr]
	sessionPath string
	infoHashes  []metainfo.Hash
	downloads   []*Download
	checkQueue  []metainfo.Hash

	// a random key for addrPort priority
	randKey []byte

	//ip4 atomic.Pointer[netip.Addr]
	//ip6 atomic.Pointer[netip.Addr]
	Config          config.Config
	connectionCount atomic.Uint32
	m               sync.RWMutex
	checkQueueLock  sync.Mutex
	fLock           sync.Mutex
	mseDisabled     bool
}

func (c *Client) AddTorrent(m *metainfo.MetaInfo, info meta.Info, downloadPath string, tags []string) error {
	log.Info().Msgf("try add torrent %s", info.Hash)

	c.m.RLock()
	if _, ok := c.downloadMap[info.Hash]; ok {
		c.m.RUnlock()
		return fmt.Errorf("torrent %s exists", info.Hash)
	}
	c.m.RUnlock()

	c.m.Lock()
	defer c.m.Unlock()

	d := c.NewDownload(m, info, downloadPath, tags)

	c.downloads = append(c.downloads, d)
	c.downloadMap[info.Hash] = d
	c.infoHashes = lo.Keys(c.downloadMap)

	tasks.Submit(d.Init)

	return nil
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

func (c *Client) addCheck(d *Download) {
	c.m.Lock()
	defer c.m.Unlock()

	c.checkQueue = append(c.checkQueue, d.info.Hash)
}

func (c *Client) checkComplete(d *Download) {
	c.m.Lock()
	defer c.m.Unlock()

	c.checkQueue = gslice.Remove(c.checkQueue, d.info.Hash)
}

func (c *Client) PeerPriority(peer netip.AddrPort) uint32 {
	if peer.Addr().Is4() {
		localV4 := c.v4Addr.Load()
		if localV4 == nil {
			return bep40.SimplePriority(c.randKey, unsafe.Bytes(peer.String()))
		}

		return bep40.Priority4(netip.AddrPortFrom(*localV4, c.Config.App.P2PPort), peer)
	}

	if peer.Addr().Is6() {
		localV6 := c.v6Addr.Load()
		if localV6 == nil {
			return bep40.SimplePriority(c.randKey, unsafe.Bytes(peer.String()))
		}

		return bep40.Priority6(netip.AddrPortFrom(*localV6, c.Config.App.P2PPort), peer)
	}

	panic(fmt.Sprintf("unexpected addrPort address format %+v", peer))
}
