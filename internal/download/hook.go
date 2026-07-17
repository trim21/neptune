// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// runHook executes a shell command asynchronously with torrent metadata
// injected via environment variables:
//
//	NEPTUNE_INFO_HASH  — hex-encoded info hash
//	NEPTUNE_NAME       — torrent name
//	NEPTUNE_SAVE_PATH  — download directory
//	NEPTUNE_SIZE       — total size in bytes
//
// An empty cmd is a no-op. If timeout > 0, the command is killed after timeout.
//
// Commands are interpreted as POSIX shell via mvdan.cc/sh, providing
// consistent behavior across platforms.
func (d *Download) runHook(cmd string, timeout time.Duration) {
	if cmd == "" {
		return
	}

	hookVars := d.hookEnv()
	name := d.info.Name
	infoHash := d.info.Hash.Hex()

	go func() {
		runCtx := d.ctx
		if timeout > 0 {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeout(d.ctx, timeout)
			defer cancel()
		}

		file, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
		if err != nil {
			log.Warn().
				Str("name", name).
				Str("info_hash", infoHash).
				Str("hook", cmd).
				Err(err).
				Msg("hook parse failed")
			return
		}

		var stderr bytes.Buffer
		runner, err := interp.New(
			interp.Env(expand.ListEnviron(append(os.Environ(), hookVars...)...)),
			interp.StdIO(os.Stdin, os.Stdout, &stderr),
		)
		if err != nil {
			log.Warn().
				Str("name", name).
				Str("info_hash", infoHash).
				Str("hook", cmd).
				Err(err).
				Msg("hook runner creation failed")
			return
		}

		err = runner.Run(runCtx, file)
		if err != nil {
			log.Warn().
				Str("name", name).
				Str("info_hash", infoHash).
				Str("hook", cmd).
				Str("stderr", stderr.String()).
				Err(err).
				Msg("hook command failed")
		} else {
			log.Debug().
				Str("name", name).
				Str("info_hash", infoHash).
				Str("hook", cmd).
				Msg("hook command succeeded")
		}
	}()
}

// hookEnv builds the environment variables for a hook command.
func (d *Download) hookEnv() []string {
	d.s.mu.RLock()
	savePath := d.s.basePath
	d.s.mu.RUnlock()

	return []string{
		"NEPTUNE_INFO_HASH=" + d.info.Hash.Hex(),
		"NEPTUNE_NAME=" + d.info.Name,
		"NEPTUNE_SAVE_PATH=" + savePath,
		fmt.Sprintf("NEPTUNE_SIZE=%d", d.info.TotalLength),
	}
}

func (d *Download) fireStartedHook() {
	cmd := d.session.Config.App.Hook.OnDownloadStarted
	if cmd == "" {
		return
	}
	log.Debug().Str("info_hash", d.info.Hash.Hex()).Msg("firing on-download-started hook")
	d.runHook(cmd, d.session.Config.App.Hook.Timeout)
}

func (d *Download) fireCompletedHook() {
	cmd := d.session.Config.App.Hook.OnDownloadCompleted
	if cmd == "" {
		return
	}
	log.Debug().Str("info_hash", d.info.Hash.Hex()).Msg("firing on-download-completed hook")
	d.runHook(cmd, d.session.Config.App.Hook.Timeout)
}
