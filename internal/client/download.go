package client

import (
	"github.com/samber/lo"

	"neptune/internal/download"
	"neptune/internal/meta"
	"neptune/internal/metainfo"
)

func (c *Client) NewDownload(m *metainfo.MetaInfo, info meta.Info, basePath string, tags []string, custom map[string]string, selectedFiles []int) *Download {
	return download.New(c.session, m, info, basePath, tags, custom, selectedFiles)
}

func (c *Client) UnmarshalResume(data []byte, totalDownloads int) error {
	d, err := download.ResumeFromData(c.session, data)
	if err != nil {
		return err
	}
	d.TrkStagger(totalDownloads)

	c.m.Lock()
	defer c.m.Unlock()
	c.downloads = append(c.downloads, d)
	c.downloadMap[d.InfoHash()] = d
	c.infoHashes = lo.Keys(c.downloadMap)

	d.Init(true, true)
	return nil
}

func (c *Client) ScheduleMove(ih metainfo.Hash, targetBasePath string) error {
	c.m.RLock()
	d, ok := c.downloadMap[ih]
	c.m.RUnlock()
	if !ok {
		return download.ErrTorrentNotFound
	}
	return d.Move(targetBasePath)
}
