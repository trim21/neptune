package web

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"

	"github.com/swaggest/usecase"

	"tyr/internal/core"
	"tyr/internal/metainfo"
	"tyr/internal/web/jsonrpc"
)

var errInvalidInfoHash = errors.New("invalid info hash")

type listTorrentFilesRequest struct {
	InfoHash string `json:"info_hash" description:"torrent file hash" required:"true"`
}

type listTorrentFilesResponse struct {
	Files []core.TorrentFile `json:"files"`
}

func listTorrentFiles(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor[*listTorrentFilesRequest, listTorrentFilesResponse](
		func(ctx context.Context, req *listTorrentFilesRequest, res *listTorrentFilesResponse) error {
			if len(req.InfoHash) != sha1.Size*2 {
				return errInvalidInfoHash
			}

			h, err := hex.DecodeString(req.InfoHash)
			if err != nil {
				return errInvalidInfoHash
			}

			res.Files = c.GetTorrentFiles(metainfo.Hash(h))

			return nil
		},
	)
	u.SetName("torrent.files")
	h.Add(u)
}
