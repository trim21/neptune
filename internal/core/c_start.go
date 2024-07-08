// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/samber/lo"
	"github.com/trim21/errgo"

	"tyr/internal/mse"
	"tyr/internal/pkg/global/tasks"
	"tyr/internal/proto"
	"tyr/internal/util"
)

func (c *Client) Start() error {
	c.initMetrics()

	if err := c.startListen(); err != nil {
		return err
	}

	go c.ch.Start()

	go func() {
		for {
			time.Sleep(time.Minute * 5)
			v4, v6, err := util.GetIpAddress()
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
			err := c.saveSession()
			c.m.RUnlock()
			if err != nil {
				fmt.Println(string(err.Stack))
			}
		}
	}()

	return nil
}

func (c *Client) startListen() error {
	var lc net.ListenConfig
	l, err := lc.Listen(c.ctx, "tcp", fmt.Sprintf(":%d", c.Config.App.P2PPort))
	if err != nil {
		return errgo.Wrap(err, "failed to listen on p2p port")
	}
	go func() {
		for {
			// it may only return timeout error, so we can ignore this
			//_ = c.sem.Acquire(context.Background(), 1)
			conn, err := l.Accept()
			if err != nil {
				c.sem.Release(1)
				continue
			}

			if !c.sem.TryAcquire(1) {
				_ = conn.Close()
				continue
			}

			c.connectionCount.Add(1)
			if c.mseDisabled {
				c.connChan <- incomingConn{
					addr: lo.Must(netip.ParseAddrPort(conn.RemoteAddr().String())),
					conn: conn,
				}
				continue
			}

			// handle mse
			go func() {
				c.m.RLock()
				keys := c.infoHashes
				c.m.RUnlock()

				rwc, err := mse.NewAccept(conn, keys, c.mseSelector)
				if err != nil {
					c.sem.Release(1)
					c.connectionCount.Sub(1)
					_ = conn.Close()
					return
				}

				c.connChan <- incomingConn{
					addr: lo.Must(netip.ParseAddrPort(conn.RemoteAddr().String())),
					conn: rwc,
				}
			}()
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
