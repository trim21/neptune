// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package store persists per-torrent resume state in a SQLite database.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	_ "modernc.org/sqlite"

	"neptune/internal/pkg/timestamp"
)

// ResumeState is the persisted download state.
// Only two meaningful states: stopped or active (Downloading/Seeding based on completion).
type ResumeState uint8

const (
	ResumeStopped ResumeState = 0
	ResumeActive  ResumeState = 1
)

//nolint:fieldalignment
type Resume struct {
	BasePath           string
	InfoHash           string
	Bitfield           []byte
	Tags               []string
	Custom             map[string]string
	Trackers           [][]string
	SelectedFiles      []int    // indices of files selected for download. nil means all files.
	FilePaths          []string // file paths (relative to BasePath), persisted to survive truncation algorithm changes
	DownloadSpeedLimit int64    // bytes per second. 0 means unlimited.
	UploadSpeedLimit   int64
	AddAt              timestamp.Timestamp
	CompletedAt        timestamp.Timestamp
	Downloaded         int64
	Uploaded           int64
	Corrupted          int64
	TrackerKey         string
	State              ResumeState
	PiecePickStrategy  uint32
	QueueWeight        int64
}

type migration struct {
	sql     string
	version int
}

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrations are applied in version order on open; a migration's statements
// run in a single transaction and user_version is bumped only after success,
// so an interrupted open re-applies remaining migrations next time.
var migrations = loadMigrations()

func loadMigrations() []migration {
	entries, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		panic(err)
	}
	sort.Strings(entries)

	out := make([]migration, 0, len(entries))
	for _, name := range entries {
		base := filepath.Base(name)
		if len(base) < 5 || base[4] != '_' {
			panic(fmt.Sprintf("store: migration file %q must be named NNNN_name.sql", name))
		}
		version, err := strconv.Atoi(base[:4])
		if err != nil {
			panic(fmt.Sprintf("store: migration file %q has invalid version prefix: %v", name, err))
		}
		data, err := migrationsFS.ReadFile(name)
		if err != nil {
			panic(err)
		}
		out = append(out, migration{sql: string(data), version: version})
	}
	return out
}

// Store locates the session database. Connections are opened on demand per
// operation and closed afterwards; the handle itself holds no open connection.
type Store struct {
	path string
}

func Open(sessionPath string) *Store {
	return &Store{path: filepath.Join(sessionPath, "session.db")}
}

func (s *Store) withDB(fn func(*sql.DB) error) error {
	db, err := sql.Open("sqlite", s.path)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx := context.Background()
	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA synchronous=NORMAL`,
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return err
		}
	}

	if err := migrate(ctx, db); err != nil {
		return err
	}

	return fn(db)
}

func migrate(ctx context.Context, db *sql.DB) error {
	var current int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return err
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, m.version)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	return nil
}

func (s *Store) Upsert(r *Resume) error {
	return s.withDB(func(db *sql.DB) error {
		ctx := context.Background()

		tags, err := json.Marshal(r.Tags)
		if err != nil {
			return err
		}
		custom, err := json.Marshal(r.Custom)
		if err != nil {
			return err
		}
		trackers, err := json.Marshal(r.Trackers)
		if err != nil {
			return err
		}
		filePaths, err := json.Marshal(r.FilePaths)
		if err != nil {
			return err
		}

		// nil SelectedFiles means "all files" and is stored as NULL so it stays
		// distinct from an explicitly empty selection.
		var selectedFiles []byte
		if r.SelectedFiles != nil {
			selectedFiles, err = json.Marshal(r.SelectedFiles)
			if err != nil {
				return err
			}
		}

		_, err = db.ExecContext(ctx, `INSERT INTO resume (
			info_hash, base_path, bitfield, tags, custom, trackers, selected_files,
			file_paths, download_speed_limit, upload_speed_limit, add_at, completed_at,
			downloaded, uploaded, corrupted, tracker_key, state, piece_pick_strategy, queue_weight
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(info_hash) DO UPDATE SET
			base_path = excluded.base_path,
			bitfield = excluded.bitfield,
			tags = excluded.tags,
			custom = excluded.custom,
			trackers = excluded.trackers,
			selected_files = excluded.selected_files,
			file_paths = excluded.file_paths,
			download_speed_limit = excluded.download_speed_limit,
			upload_speed_limit = excluded.upload_speed_limit,
			add_at = excluded.add_at,
			completed_at = excluded.completed_at,
			downloaded = excluded.downloaded,
			uploaded = excluded.uploaded,
			corrupted = excluded.corrupted,
			tracker_key = excluded.tracker_key,
			state = excluded.state,
			piece_pick_strategy = excluded.piece_pick_strategy,
			queue_weight = excluded.queue_weight`,
			r.InfoHash,
			r.BasePath,
			r.Bitfield,
			tags,
			custom,
			trackers,
			selectedFiles,
			filePaths,
			r.DownloadSpeedLimit,
			r.UploadSpeedLimit,
			r.AddAt.UnixNano(),
			r.CompletedAt.UnixNano(),
			r.Downloaded,
			r.Uploaded,
			r.Corrupted,
			r.TrackerKey,
			r.State,
			r.PiecePickStrategy,
			r.QueueWeight,
		)
		return err
	})
}

func (s *Store) Delete(infoHash string) error {
	return s.withDB(func(db *sql.DB) error {
		_, err := db.ExecContext(context.Background(), `DELETE FROM resume WHERE info_hash = ?`, infoHash)
		return err
	})
}

func (s *Store) Count() (int, error) {
	var n int
	err := s.withDB(func(db *sql.DB) error {
		return db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM resume`).Scan(&n)
	})
	return n, err
}

func (s *Store) All() ([]Resume, error) {
	var out []Resume
	err := s.withDB(func(db *sql.DB) error {
		ctx := context.Background()
		rows, err := db.QueryContext(ctx, `SELECT
			info_hash, base_path, bitfield, tags, custom, trackers, selected_files,
			file_paths, download_speed_limit, upload_speed_limit, add_at, completed_at,
			downloaded, uploaded, corrupted, tracker_key, state, piece_pick_strategy, queue_weight
		FROM resume`)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var (
				r                  Resume
				tags, custom       []byte
				trackers           []byte
				selectedFiles      []byte
				filePaths          []byte
				addAt, completedAt int64
			)
			if err := rows.Scan(
				&r.InfoHash,
				&r.BasePath,
				&r.Bitfield,
				&tags,
				&custom,
				&trackers,
				&selectedFiles,
				&filePaths,
				&r.DownloadSpeedLimit,
				&r.UploadSpeedLimit,
				&addAt,
				&completedAt,
				&r.Downloaded,
				&r.Uploaded,
				&r.Corrupted,
				&r.TrackerKey,
				&r.State,
				&r.PiecePickStrategy,
				&r.QueueWeight,
			); err != nil {
				return err
			}

			if err := json.Unmarshal(tags, &r.Tags); err != nil {
				return err
			}
			if err := json.Unmarshal(custom, &r.Custom); err != nil {
				return err
			}
			if err := json.Unmarshal(trackers, &r.Trackers); err != nil {
				return err
			}
			if selectedFiles != nil {
				if err := json.Unmarshal(selectedFiles, &r.SelectedFiles); err != nil {
					return err
				}
			}
			if err := json.Unmarshal(filePaths, &r.FilePaths); err != nil {
				return err
			}

			r.AddAt = timestamp.New(time.Unix(0, addAt))
			r.CompletedAt = timestamp.New(time.Unix(0, completedAt))

			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
