// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build (windows && (amd64 || arm64)) || (darwin && (amd64 || arm64)) || (linux && (amd64 || arm64))

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/KimMachineGun/automemlimit/memlimit"
	"github.com/gofrs/flock"
	"github.com/pelletier/go-toml/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/samber/lo"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/trim21/conntrack"
	"github.com/trim21/errgo"
	"go.uber.org/automaxprocs/maxprocs"
	"gopkg.in/natefinch/lumberjack.v2"

	"neptune/internal/config"
	"neptune/internal/core"
	"neptune/internal/meta"
	"neptune/internal/metainfo"
	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/global"
	"neptune/internal/pkg/random"
	"neptune/internal/pkg/sys"
	"neptune/internal/version"
	"neptune/internal/web"
)

func main() {
	setupFlagsAndEnvParser()
	setupMetrics()

	debug := viper.GetBool("debug")
	if debug {
		//runtime.SetBlockProfileRate(10000)
		//runtime.SetMutexProfileFraction(10000)
		_, _ = fmt.Fprintln(os.Stderr, "enable debug mode")
	}

	sessionPath := mustGetSessionPath()

	createSessionDirectory(sessionPath)

	global.Init(debug)

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

	initResourceLimit()

	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", address)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "failed to start heep server")
		os.Exit(1)
	}

	app := core.New(cfg, sessionPath, debug)

	if e := app.Start(); e != nil {
		errExit("failed to listen on p2p port", e)
	}

	var done = make(chan empty.Empty)

	go func() {
		server := web.New(app, webToken, debug)
		fmt.Println("start", "http://"+address)

		if debug {
			listener = conntrack.NewListener(listener, conntrack.TrackWithTracing(), conntrack.TrackWithName("rpc"))
		} else {
			listener = conntrack.NewListener(listener, conntrack.TrackWithName("rpc"))
		}

		if err := http.Serve(listener, server); err != nil {
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

	if global.Dev {
		log.Debug().Msg("add debug torrent")
		if sys.IsWindows {
			//{
			//	lo.Must0(os.RemoveAll("D:\\downloads\\2"))
			//	m := lo.Must(metainfo.LoadFromFile(`C:\Users\Trim21\Downloads\2.torrent`))
			//	lo.Must0(app.AddTorrent(m, lo.Must(meta.FromTorrent(*m)), "D:\\Downloads\\2", []string{}))
			//}

			{
				//lo.Must0(os.RemoveAll("D:\\downloads\\ubuntu"))
				m := lo.Must(metainfo.LoadFromFile(`C:\Users\Trim21\Downloads\ubuntu-24.04-desktop-amd64.iso.torrent.patched`))
				lo.Must0(app.AddTorrent(m, lo.Must(meta.FromTorrent(*m)), "D:\\Downloads\\ubuntu", []string{}))
			}
		}

		//if sys.IsLinux {
		//	lo.Must0(os.RemoveAll("/export/ssd-2t/try/2"))
		//	m := lo.Must(metainfo.LoadFromFile(`/export/ssd-2t/2.torrent`))
		//	lo.Must0(app.addTorrent(m, lo.Must(meta.FromTorrent(*m)), "/export/ssd-2t/try/2", nil))
		//}
	}

	<-done
	fmt.Println("shutting down...")
	app.Shutdown()
}

func setupMetrics() {
	// Make sure all outbound connections use the wrapped dialer.
	http.DefaultTransport = &http.Transport{DialContext: conntrack.NewDialContextFunc(
		conntrack.DialWithTracing(),
		conntrack.DialWithDialer(&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 10 * time.Second,
		}),
	)}

	if prometheus.Unregister(collectors.NewGoCollector()) {
		// https://pkg.go.dev/runtime/metrics
		prometheus.MustRegister(collectors.NewGoCollector(
			collectors.WithGoCollectorRuntimeMetrics(
				//collectors.MetricsAll,
				//collectors.GoRuntimeMetricsRule{},

				collectors.MetricsGC,
				collectors.MetricsMemory,
				collectors.MetricsScheduler,
				collectors.GoRuntimeMetricsRule{Matcher: regexp.MustCompile("^/cgo/.*")},
			),
			//collectors.WithoutGoCollectorRuntimeMetrics(
			//	regexp.MustCompile("^/godebug/.*"),
			//	regexp.MustCompile("^/cpu/.*"),
			//),
		))
	}
}

func setupFlagsAndEnvParser() {
	if slices.Contains(os.Args[1:], "-v") {
		_, _ = fmt.Fprintln(os.Stderr, version.Version)
		os.Exit(0)
		return
	}

	if slices.Contains(os.Args[1:], "--version") {
		_, _ = fmt.Fprintln(os.Stderr, version.Print())
		os.Exit(0)
		return
	}

	pflag.String("session-path", "", "client session path (default ~/.neptune/)")
	pflag.String("config-file", "", "path to config file (default {session-path}/config.toml)")

	pflag.String("web", "127.0.0.1:8002", "web interface address")
	pflag.String("web-secret-token", "", "web interface address secret token")
	pflag.Uint16("p2p-port", 50047, "p2p listen port")

	pflag.Bool("log-json", false, "log as json format")
	pflag.String("log-level", "info", "log level")
	pflag.Bool("log-save-to-file", true, "also write log to {session-path/logs/app.log")

	pflag.Bool("debug", false, "enable debug mode")

	pflag.Bool("version", false, "show version")
	pflag.Bool("v", false, "show version, short")

	// this avoids 'pflag: help requested' error when calling for help message.
	if slices.Contains(os.Args[1:], "--help") || slices.Contains(os.Args[1:], "-h") {
		pflag.Usage()
		_, _ = fmt.Fprintln(os.Stderr, "\nNote: command arguments will override config file, but won't change config file.")
		os.Exit(0)
		return
	}

	pflag.Parse()

	viper.SetEnvPrefix("NEPTUNE")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()

	lo.Must0(viper.BindPFlags(pflag.CommandLine), "failed to parse combine argument with env")
}

func defaultSessionPath() string {
	h, err := os.UserHomeDir()
	if err != nil {
		errExit("failed to get home directory, please set session path with --session-path manually", err)
	}

	return filepath.Join(h, ".neptune")
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
		_, _ = fmt.Fprintf(os.Stderr, "try remove %q if no other neptune instance is running\n", lockPath)
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

	zerolog.ErrorStackMarshaler = func(err error) any {
		s, ok := err.(errgo.Stack)
		if ok {
			return s.Stack()
		}

		return err
	}

	log.Logger = log.Output(w).Level(logLevel).With().Stack().Logger()

	switch {
	case log.Trace().Enabled():
		log.Trace().Msg("enable trace level logging")
	case log.Debug().Enabled():
		log.Debug().Msg("enable debug level logging")
	case log.Info().Enabled():
		log.Info().Msg("enable info level logging")
	}
}

func mustParseConfig(sessionPath string) config.Config {
	configFilePath := viper.GetString("config-file")
	if configFilePath == "" {
		configFilePath = filepath.Join(sessionPath, "config.toml")
	}

	cfg, err := config.LoadFromFile(configFilePath)
	if err != nil {
		var derr *toml.DecodeError
		if errors.As(err, &derr) {
			_, _ = fmt.Fprintln(os.Stderr, derr.String())
			row, col := derr.Position()
			_, _ = fmt.Fprintln(os.Stderr, "error occurred at row", row, "column", col)
			os.Exit(2)
		}

		errExit("failed to load config", err)
	}

	cfg.App.P2PPort = viper.GetUint16("p2p-port")

	return cfg
}

func initResourceLimit() {
	if sys.IsLinux {
		if _, err := maxprocs.Set(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "Failed to set GOMAXPROCS automatically.")
			_, _ = fmt.Fprintln(os.Stderr, "Consider to set env manually if you are running process with cgroup.")
			_, _ = fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}

		if _, err := memlimit.SetGoMemLimitWithOpts(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "Failed to set GOMEMLIMIT automatically.")
			_, _ = fmt.Fprintln(os.Stderr, "Consider to set env manually if you are running process with cgroup.")
			_, _ = fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
	}
}
