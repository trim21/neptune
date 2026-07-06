// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/samber/lo"
	"github.com/trim21/conntrack"

	"neptune/internal/pkg/global"
	"neptune/internal/pkg/global/tasks"
	"neptune/internal/proto"
	"neptune/internal/util"
)

func (c *Client) Start() error {
	if err := c.startListen(); err != nil {
		return err
	}

	c.startGlobalLoops()

	// TODO: impl
	if !global.Dev {
		go func() {
			for {
				time.Sleep(time.Minute * 30)
				c.scrape()
			}
		}()
	}

	go func() {
		for {
			time.Sleep(time.Minute * 5)
			v4, v6, err := util.GetIPAddress()
			if err != nil {
				log.Err(err).Msg("failed to get local ip address")
				continue
			}

			// normally it's not safe to simply get value from atomic.Pointer then set is.
			// But we would write it, so it's safe.
			if v4 != nil {
				p := c.ipv4.Load()
				if p == nil || *p != *v4 {
					log.Info().Msgf("new ipv4 address: %s", v4)
					c.ipv4.Store(v4)
				}
			}

			if v6 != nil {
				p := c.ipv6.Load()
				if p == nil || *p != *v6 {
					log.Info().Msgf("new ipv6 address: %s", v6)
					c.ipv6.Store(v6)
				}
			}
		}
	}()

	go func() {
		for {
			time.Sleep(time.Minute * 10)
			c.m.RLock()
			log.Info().Msg("save session")
			err := c.saveSessionUnsafe()
			c.m.RUnlock()
			if err != nil {
				fmt.Println(string(err.Stack)) //nolint: forbidigo
			}
		}
	}()

	// pre-scan resume files to get total count for staggered announces
	var resumeFiles []string
	if err := filepath.Walk(c.resumePath, func(path string, info fs.FileInfo, _ error) error {
		if info != nil && !info.IsDir() && strings.HasSuffix(path, ".resume") {
			resumeFiles = append(resumeFiles, path)
		}
		return nil
	}); err != nil {
		return err
	}

	totalDownloads := len(resumeFiles)
	for _, path := range resumeFiles {
		resumeData, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := c.UnmarshalResume(resumeData, totalDownloads); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) startListen() error {
	// The listener was already bound during port resolution in New().
	// Wrap it with conntrack for tracing and use it directly — no rebind.
	l := conntrack.NewListener(c.p2pListener, conntrack.TrackWithTracing(), conntrack.TrackWithName("p2p"))

	go c.handleConn()

	go func() {
		for {
			// it may only return timeout error, so we can ignore this
			// _ = c.sem.Acquire(context.Background(), 1)
			conn, err := l.Accept()
			if err != nil {
				c.sem.Release(1)
				continue
			}

			if !c.sem.TryAcquire(1) {
				_ = conn.Close()
				continue
			}

			// peers is wrapped by conntrack, so we need to cast it with interface
			if tcp, ok := conn.(interface{ SetLinger(sec int) error }); ok {
				_ = tcp.SetLinger(0)
			}

			_ = conn.SetDeadline(time.Now().Add(global.ConnTimeout))

			c.connectionCount.Add(1)

			// TODO: support MSE
			// if c.mseDisabled {
			c.connChan <- incomingConn{
				addr: lo.Must(netip.ParseAddrPort(conn.RemoteAddr().String())),
				conn: conn,
			}
			// continue
			//}

			//// handle mse
			// go func() {
			//	c.m.RLock()
			//	keys := c.infoHashes
			//	c.m.RUnlock()
			//
			//	rwc, err := mse.NewAccept(peers, keys, c.mseSelector)
			//	if err != nil {
			//		c.sem.Release(1)
			//		c.connectionCount.Sub(1)
			//		_ = peers.Close()
			//		return
			//	}
			//
			//	c.connChan <- incomingConn{
			//		addr: lo.Must(netip.ParseAddrPort(peers.RemoteAddr().String())),
			//		peers: rwc,
			//	}
			// }()
		}
	}()
	return nil
}

func (c *Client) handleConn() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case conn := <-c.connChan:
			tasks.Submit(func() {
				h, err := proto.ReadHandshake(conn.conn)
				if err != nil {
					c.sem.Release(1)
					c.connectionCount.Sub(1)
					_ = conn.conn.Close()
					return
				}

				log.Debug().Stringer("info_hash", h.InfoHash).Msg("incoming connection")

				c.m.RLock()
				defer c.m.RUnlock()

				d, ok := c.downloadMap[h.InfoHash]
				if !ok {
					c.sem.Release(1)
					c.connectionCount.Sub(1)
					_ = conn.conn.Close()
					return
				}

				d.AddConn(conn.addr, conn.conn, h)
			})
		}
	}
}
