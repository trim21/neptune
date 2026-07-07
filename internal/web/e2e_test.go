// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package web_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"neptune/internal/client"
	"neptune/internal/config"
	"neptune/internal/web"
)

const (
	keyInfoHash = "info_hash"
	keyTags     = "tags"
	keyLimit    = "limit"
)

type jsonrpcReq struct {
	Params  any    `json:"params,omitempty"`
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	ID      int    `json:"id"`
}

type jsonrpcResp struct {
	Error   *jsonrpcErr     `json:"error,omitempty"`
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	ID      int             `json:"id"`
}

type jsonrpcErr struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

func newRequest(method string, params any) jsonrpcReq {
	return jsonrpcReq{JSONRPC: "2.0", Method: method, Params: params, ID: 1}
}

func makeJSONRPCRequest(t *testing.T, url, token, method string, params any) jsonrpcResp {
	t.Helper()

	body, err := json.Marshal(newRequest(method, params))
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", token)
	}

	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()

	var rpcResp jsonrpcResp
	err = json.NewDecoder(res.Body).Decode(&rpcResp)
	require.NoError(t, err)

	return rpcResp
}

func requireNoRPCError(t *testing.T, resp jsonrpcResp) {
	t.Helper()
	require.Nil(t, resp.Error, "unexpected RPC error: %+v", resp.Error)
}

func requireRPCError(t *testing.T, resp jsonrpcResp, expectedCode int) {
	t.Helper()
	require.NotNil(t, resp.Error)
	require.Equal(t, expectedCode, resp.Error.Code, "error message: %s", resp.Error.Message)
}

func TestE2E(t *testing.T) {
	tmpDir := t.TempDir()

	torrentPath := filepath.Join("..", "metainfo", "testdata", "archlinux-2011.08.19-netinstall-i686.iso.torrent")
	torrentData, err := os.ReadFile(torrentPath)
	require.NoError(t, err)

	downloadDir := filepath.Join(tmpDir, "downloads")
	sessionPath := filepath.Join(tmpDir, "session")
	require.NoError(t, os.MkdirAll(filepath.Join(sessionPath, "torrents"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(sessionPath, "resume"), 0755))
	require.NoError(t, os.MkdirAll(downloadDir, 0755))

	cfg := config.Config{
		App: config.Application{
			DownloadDir:              downloadDir,
			MaxHTTPParallel:          10,
			P2PPort:                  0,
			NumWant:                  50,
			GlobalConnectionLimit:    10,
			GlobalUploadSlots:        4,
			GlobalDownloadSpeedLimit: 0,
			GlobalUploadSpeedLimit:   0,
		},
	}

	cl := client.New(cfg, sessionPath, false)
	defer cl.Shutdown()

	token := "test-token-e2e"
	handler := web.New(cl, token, false)
	server := httptest.NewServer(handler)
	defer server.Close()

	url := server.URL + "/json_rpc"

	var infoHash string

	// ---------------------------------------------------------------------------
	// system.ping
	// ---------------------------------------------------------------------------
	t.Run("system.ping", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "system.ping", struct{}{})
		requireNoRPCError(t, resp)
	})

	// ---------------------------------------------------------------------------
	// transfer_summary (no torrents)
	// ---------------------------------------------------------------------------
	t.Run("transfer_summary_empty", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "transfer_summary", struct{}{})
		requireNoRPCError(t, resp)
	})

	// ---------------------------------------------------------------------------
	// torrent.list (empty)
	// ---------------------------------------------------------------------------
	t.Run("torrent.list_empty", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.list", struct{}{})
		requireNoRPCError(t, resp)
		var r struct {
			Torrents []any `json:"torrents"`
		}
		require.NoError(t, json.Unmarshal(resp.Result, &r))
		require.Empty(t, r.Torrents)
	})

	// ---------------------------------------------------------------------------
	// torrent.add
	// ---------------------------------------------------------------------------
	t.Run("torrent.add", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.add", map[string]any{
			"torrent_file": torrentData,
			keyTags:        []string{"test", "e2e"},
		})
		requireNoRPCError(t, resp)

		var r map[string]string
		require.NoError(t, json.Unmarshal(resp.Result, &r))
		infoHash = r[keyInfoHash]
		require.Len(t, infoHash, 40)
	})

	// Wait for async Init to finish
	time.Sleep(100 * time.Millisecond)

	// ---------------------------------------------------------------------------
	// torrent.get
	// ---------------------------------------------------------------------------
	t.Run("torrent.get", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.get", map[string]string{
			keyInfoHash: infoHash,
		})
		requireNoRPCError(t, resp)

		var r map[string]any
		require.NoError(t, json.Unmarshal(resp.Result, &r))
		require.Equal(t, "archlinux-2011.08.19-netinstall-i686.iso", r["name"])
		require.NotNil(t, r[keyTags])
	})

	// ---------------------------------------------------------------------------
	// torrent.list (with one torrent)
	// ---------------------------------------------------------------------------
	t.Run("torrent.list_nonempty", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.list", struct{}{})
		requireNoRPCError(t, resp)

		var r struct {
			Torrents []map[string]any `json:"torrents"`
		}
		require.NoError(t, json.Unmarshal(resp.Result, &r))
		require.Len(t, r.Torrents, 1)
		require.Equal(t, infoHash, r.Torrents[0]["hash"])
	})

	// ---------------------------------------------------------------------------
	// torrent.files
	// ---------------------------------------------------------------------------
	t.Run("torrent.files", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.files", map[string]string{
			keyInfoHash: infoHash,
		})
		requireNoRPCError(t, resp)
	})

	// ---------------------------------------------------------------------------
	// torrent.peers
	// ---------------------------------------------------------------------------
	t.Run("torrent.peers", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.peers", map[string]string{
			keyInfoHash: infoHash,
		})
		requireNoRPCError(t, resp)
	})

	// ---------------------------------------------------------------------------
	// torrent.trackers
	// ---------------------------------------------------------------------------
	t.Run("torrent.trackers", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.trackers", map[string]string{
			keyInfoHash: infoHash,
		})
		requireNoRPCError(t, resp)
	})

	// ---------------------------------------------------------------------------
	// torrent.add_tags
	// ---------------------------------------------------------------------------
	t.Run("torrent.add_tags", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.add_tags", map[string]any{
			keyInfoHash: infoHash,
			keyTags:     []string{"added"},
		})
		requireNoRPCError(t, resp)

		// Verify tag was added
		resp = makeJSONRPCRequest(t, url, token, "torrent.get", map[string]string{
			keyInfoHash: infoHash,
		})
		requireNoRPCError(t, resp)
		var r map[string]any
		require.NoError(t, json.Unmarshal(resp.Result, &r))
		tags, ok := r[keyTags].([]any)
		require.True(t, ok)
		require.Contains(t, tags, "added")
	})

	// ---------------------------------------------------------------------------
	// torrent.remove_tags
	// ---------------------------------------------------------------------------
	t.Run("torrent.remove_tags", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.remove_tags", map[string]any{
			keyInfoHash: infoHash,
			keyTags:     []string{"added"},
		})
		requireNoRPCError(t, resp)

		// Verify tag was removed
		resp = makeJSONRPCRequest(t, url, token, "torrent.get", map[string]string{
			keyInfoHash: infoHash,
		})
		requireNoRPCError(t, resp)
		var r map[string]any
		require.NoError(t, json.Unmarshal(resp.Result, &r))
		tags, ok := r[keyTags].([]any)
		require.True(t, ok)
		require.NotContains(t, tags, "added")
	})

	// ---------------------------------------------------------------------------
	// torrent.set_file_priority
	// ---------------------------------------------------------------------------
	t.Run("torrent.set_file_priority", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.set_file_priority", map[string]any{
			keyInfoHash: infoHash,
			"file_ids":  []int{0},
			"priority":  0,
		})
		requireNoRPCError(t, resp)
	})

	// ---------------------------------------------------------------------------
	// torrent.set_download_limit
	// ---------------------------------------------------------------------------
	t.Run("torrent.set_download_limit", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.set_download_limit", map[string]any{
			keyInfoHash: infoHash,
			keyLimit:    int64(1024 * 1024),
		})
		requireNoRPCError(t, resp)
	})

	// ---------------------------------------------------------------------------
	// torrent.set_upload_limit
	// ---------------------------------------------------------------------------
	t.Run("torrent.set_upload_limit", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.set_upload_limit", map[string]any{
			keyInfoHash: infoHash,
			keyLimit:    int64(512 * 1024),
		})
		requireNoRPCError(t, resp)
	})

	// ---------------------------------------------------------------------------
	// torrent.stop
	// ---------------------------------------------------------------------------
	t.Run("torrent.stop", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.stop", map[string]string{
			keyInfoHash: infoHash,
		})
		requireNoRPCError(t, resp)
	})

	// ---------------------------------------------------------------------------
	// torrent.start
	// ---------------------------------------------------------------------------
	t.Run("torrent.start", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.start", map[string]string{
			keyInfoHash: infoHash,
		})
		requireNoRPCError(t, resp)
	})

	// ---------------------------------------------------------------------------
	// torrent.stop (for resume test)
	// ---------------------------------------------------------------------------
	t.Run("torrent.stop_before_resume", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.stop", map[string]string{
			keyInfoHash: infoHash,
		})
		requireNoRPCError(t, resp)
	})

	// ---------------------------------------------------------------------------
	// torrent.start (idempotent — already started)
	// ---------------------------------------------------------------------------
	t.Run("torrent.start_idempotent", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.start", map[string]string{
			keyInfoHash: infoHash,
		})
		requireNoRPCError(t, resp)
	})

	// ---------------------------------------------------------------------------
	// client.set_download_limit
	// ---------------------------------------------------------------------------
	t.Run("client.set_download_limit", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "client.set_download_limit", map[string]any{
			keyLimit: int64(2048 * 1024),
		})
		requireNoRPCError(t, resp)
	})

	// ---------------------------------------------------------------------------
	// client.set_upload_limit
	// ---------------------------------------------------------------------------
	t.Run("client.set_upload_limit", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "client.set_upload_limit", map[string]any{
			keyLimit: int64(1024 * 1024),
		})
		requireNoRPCError(t, resp)
	})

	// ---------------------------------------------------------------------------
	// torrent.remove
	// ---------------------------------------------------------------------------
	t.Run("torrent.remove", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.remove", map[string]any{
			keyInfoHash:   infoHash,
			"delete_data": true,
		})
		requireNoRPCError(t, resp)
	})

	// ---------------------------------------------------------------------------
	// torrent.list (empty after remove)
	// ---------------------------------------------------------------------------
	t.Run("torrent.list_after_remove", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.list", struct{}{})
		requireNoRPCError(t, resp)
		var r struct {
			Torrents []any `json:"torrents"`
		}
		require.NoError(t, json.Unmarshal(resp.Result, &r))
		require.Empty(t, r.Torrents)
	})

	// ---------------------------------------------------------------------------
	// Error: invalid token
	// ---------------------------------------------------------------------------
	t.Run("error_invalid_token", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, "wrong-token", "system.ping", struct{}{})
		requireRPCError(t, resp, -32600)
	})

	// ---------------------------------------------------------------------------
	// Error: invalid method
	// ---------------------------------------------------------------------------
	t.Run("error_invalid_method", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "nonexistent.method", struct{}{})
		requireRPCError(t, resp, -32601)
	})

	// ---------------------------------------------------------------------------
	// Error: invalid info_hash (non-hex chars, triggers hex.DecodeString error)
	// ---------------------------------------------------------------------------
	t.Run("error_invalid_infohash", func(t *testing.T) {
		resp := makeJSONRPCRequest(t, url, token, "torrent.get", map[string]string{
			keyInfoHash: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		})
		requireRPCError(t, resp, 1)
	})

	t.Logf("E2E test completed. InfoHash from torrent.add: %s", infoHash)
}
