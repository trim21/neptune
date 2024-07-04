// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"net/url"

	"github.com/anacrolix/torrent/bencode"
	"github.com/samber/lo"

	"tyr/internal/metainfo"
	"tyr/internal/pkg/null"
)

type scrapeResponse struct {
	Files map[string]scrapeResponseFile `bencode:"files"`
}

type scrapeResponseFile struct {
	FailureReason null.String `bencode:"failure reason"`
	Complete      int         `bencode:"complete"`
	Downloaded    int         `bencode:"downloaded"`
	Incomplete    int         `bencode:"incomplete"`
}

//var p = pool.New(func() map[string][]metainfo.Hash {
//	// 1000 url length limit / 70 per info_hash
//	return make(map[string][]metainfo.Hash, 20)
//})

func (c *Client) scrape() {
	r := c.http.R()

	var m = make(map[string][]metainfo.Hash, 20)
	//defer p.Put(m)
	//clear(m)

	c.m.RLock()
	for h, d := range c.downloadMap {
		d.m.RLock()
		if !(d.state == Downloading || d.state == Uploading) {
			d.m.RUnlock()
			continue
		}
		m[d.ScrapeUrl()] = append(m[d.ScrapeUrl()], h)
		d.m.RUnlock()
	}

	c.m.RUnlock()
	r.QueryParam = url.Values{"info_hash": []string{}}
}

func init() {
	var s scrapeResponse

	lo.Must0(bencode.Unmarshal([]byte("d5:filesd30:\xc2\xbd\xc2\x89<\xc2\x9bH\xc2\xb5\xc2\x95}\xc2\xb5\x18\x0es'\xc2\x90\xc2\xb5\xc2\xbe.\xc2\xa2\x14:d8:completei972e10:downloadedi2811e10:incompletei2eeee"), &s))
	//fmt.Println(err)
	//spew.Dump(s)
}
