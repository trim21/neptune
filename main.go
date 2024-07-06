// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/gofrs/flock"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/samber/lo"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/automaxprocs/maxprocs"
	"gopkg.in/natefinch/lumberjack.v2"

	"tyr/internal/config"
	"tyr/internal/core"
	"tyr/internal/meta"
	"tyr/internal/metainfo"
	"tyr/internal/pkg/empty"
	"tyr/internal/pkg/global"
	"tyr/internal/pkg/random"
	_ "tyr/internal/platform" // deny compile on unsupported platform
	"tyr/internal/web"
)

func main() {
	setupFlagsAndEnvParser()

	debug := viper.GetBool("debug")
	if debug {
		//runtime.SetBlockProfileRate(10000)
		_, _ = fmt.Fprintln(os.Stderr, "enable debug mode")
	}

	sessionPath := mustGetSessionPath()

	createSessionDirectory(sessionPath)

	fileLock := mustLockSessionDirectory(filepath.Join(sessionPath, ".lock"))
	// We do not actually need to unlock it, when process dead, OS will unlock it automatically.
	// But we need to keep a reference to lock object GC won't close underlying file.
	// If fd is closed, OS will unlock this lock.
	defer fileLock.Unlock()

	setupLogger(sessionPath)

	cfg := mustParseConfig(sessionPath)

	address := viper.GetString("web")
	webToken := viper.GetString("web-secret-token")

	if webToken == "" {
		webToken = random.UrlSafeStr(32)
		_, _ = fmt.Fprintf(os.Stderr, "web secret token is empty, generating new token: %s\n", webToken)
	}

	if global.IsLinux {
		if _, err := maxprocs.Set(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "Failed to set GOMAXPROCS automatically.")
			_, _ = fmt.Fprintln(os.Stderr, "Consider to set env manually if you are running with cgroup.")
			_, _ = fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
	}

	app := core.New(cfg, sessionPath)

	if e := app.Start(); e != nil {
		errExit("failed to listen on p2p port", e)
	}

	//{
	//	m := lo.Must(metainfo.LoadFromFile(`C:\Users\Trim21\Downloads\ubuntu-24.04-desktop-amd64.iso.torrent.patched`))
	//	lo.Must0(app.AddTorrent(m, lo.Must(meta.FromTorrent(*m)), "D:\\Downloads\\ubuntu", nil))
	//}

	if global.Dev {
		if global.IsWindows {
			//lo.Must0(os.RemoveAll("D:\\downloads\\2"))
			m := lo.Must(metainfo.LoadFromFile(`C:\Users\Trim21\Downloads\2.torrent`))
			lo.Must0(app.AddTorrent(m, lo.Must(meta.FromTorrent(*m)), "D:\\Downloads\\2", []string{}))
		}

		if global.IsLinux {
			lo.Must0(os.RemoveAll("/export/ssd-2t/try/2"))
			m := lo.Must(metainfo.LoadFromFile(`/export/ssd-2t/2.torrent`))
			lo.Must0(app.AddTorrent(m, lo.Must(meta.FromTorrent(*m)), "/export/ssd-2t/try/2", nil))
		}
	}

	var done = make(chan empty.Empty)

	go func() {
		server := web.New(app, webToken, debug)
		fmt.Println("start", "http://"+address)
		if err := http.ListenAndServe(address, server); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
		}
		done <- empty.Empty{}
	}()

	signalChan := make(chan os.Signal, 1)

	signal.Notify(
		signalChan,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGTERM,
	)

	go func() {
		<-signalChan
		done <- empty.Empty{}
	}()

	<-done
	fmt.Println("shutting down...")
	app.Shutdown()
}

func setupFlagsAndEnvParser() {
	pflag.String("session-path", "", "client session path (default ~/.tyr/)")
	pflag.String("config-file", "", "path to config file (default {session-path}/config.toml)")

	pflag.String("web", "127.0.0.1:8002", "web interface address")
	pflag.String("web-secret-token", "", "web interface address secret token")
	pflag.Uint16("p2p-port", 50047, "p2p listen port")

	pflag.Bool("log-json", false, "log as json format")
	pflag.String("log-level", "error", "log level")
	pflag.Bool("log-save-to-file", true, "also write log to {session-path/logs/app.log")

	pflag.Bool("debug", false, "enable debug mode")

	// this avoids 'pflag: help requested' error when calling for help message.
	if slices.Contains(os.Args[1:], "--help") || slices.Contains(os.Args[1:], "-h") {
		pflag.Usage()
		_, _ = fmt.Fprintln(os.Stderr, "\nNote: command arguments will override config file, but won't change config file.")
		os.Exit(0)
		return
	}

	pflag.Parse()

	viper.SetEnvPrefix("TYR")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()

	lo.Must0(viper.BindPFlags(pflag.CommandLine), "failed to parse combine argument with env")
}

func defaultSessionPath() string {
	h, err := os.UserHomeDir()
	if err != nil {
		errExit("failed to get home directory, please set session path with --session-path manually", err)
	}

	return filepath.Join(h, ".tyr")
}

func errExit(msg ...any) {
	_, _ = fmt.Fprintln(os.Stderr, msg...)
	os.Exit(1)
}

func createSessionDirectory(sessionPath string) {
	for _, dir := range []string{
		filepath.Join(sessionPath, "torrents"),
		filepath.Join(sessionPath, "resume"),
		filepath.Join(sessionPath, "logs"),
	} {
		err := os.MkdirAll(dir, os.ModePerm)
		if err != nil {
			errExit("fail to create directory for session", err)
		}
	}
}

func mustGetSessionPath() string {
	sessionPath := viper.GetString("session-path")

	if sessionPath == "" {
		sessionPath = defaultSessionPath()
	} else {
		if strings.HasPrefix(sessionPath, "~/") || strings.HasPrefix(sessionPath, `~\`) {
			h, err := os.UserHomeDir()
			if err != nil {
				errExit("failed to get home directory, please set session path with --session-path manually", err)
			}

			sessionPath = strings.Replace(sessionPath, "~", h, 1)
		}
	}

	return sessionPath
}

func mustLockSessionDirectory(lockPath string) *flock.Flock {
	fileLock := flock.New(lockPath)
	locked, err := fileLock.TryLock()
	if err != nil {
		errExit("can't acquire lock:", err)
		return nil
	}
	if !locked {
		_, _ = fmt.Fprintln(os.Stderr, "can't acquire lock, maybe another process is running")
		_, _ = fmt.Fprintf(os.Stderr, "try remove %q if no other tyr instance is running\n", lockPath)
		os.Exit(1)
		return nil
	}

	return fileLock
}

func parseLogLevel(s string) zerolog.Level {
	switch strings.ToLower(s) {
	case "trace":
		return zerolog.TraceLevel
	case "debug":
		return zerolog.DebugLevel
	case "info":
		return zerolog.InfoLevel
	case "warn":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	}

	errExit(fmt.Sprintf("unknown log level %q, only trace/debug/info/warn/error is allowed", s))

	return zerolog.NoLevel
}

func setupLogger(sessionPath string) {
	jsonLog := viper.GetBool("log-json")
	saveLogFile := viper.GetBool("log-save-to-file")
	logLevel := parseLogLevel(viper.GetString("log-level"))

	var w io.Writer = os.Stdout

	if !jsonLog {
		w = zerolog.ConsoleWriter{Out: os.Stdout}
	}

	if saveLogFile {
		rotation := &lumberjack.Logger{
			Filename:   filepath.Join(sessionPath, "logs", "app.log"),
			MaxSize:    10, // megabytes
			MaxBackups: 3,
			MaxAge:     28, //days
		}
		w = zerolog.MultiLevelWriter(rotation, w)
	}

	log.Logger = log.Output(w).Level(logLevel)
}

func mustParseConfig(sessionPath string) config.Config {
	configFilePath := viper.GetString("config-file")
	if configFilePath == "" {
		configFilePath = filepath.Join(sessionPath, "config.toml")
	}

	cfg, err := config.LoadFromFile(configFilePath)
	if err != nil {
		errExit("failed to load config", err)
	}

	cfg.App.P2PPort = viper.GetUint16("p2p-port")

	return cfg
}
