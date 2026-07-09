// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/pelletier/go-toml/v2"
	"github.com/trim21/errgo"
	lua "github.com/yuin/gopher-lua"
)

// LoadFromTOML loads the base config from a TOML file.
// Moved from config.go to keep the public API clean.
func LoadFromTOML(path string) (Config, error) {
	var cfg = Config{
		App: Application{MaxHTTPParallel: 100, GlobalConnectionLimit: 500, MaxRequestBodySize: 50 << 20},
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}

		return Config{}, errgo.Wrap(err, "failed to read config file")
	}
	defer f.Close()

	if err := toml.NewDecoder(f).DisallowUnknownFields().Decode(&cfg); err != nil {
		return cfg, errgo.Wrap(err, "failed to parse config file")
	}

	applyDefaults(&cfg.App)

	return cfg, nil
}

// LoadFromLua loads config entirely from a Lua script, starting with defaults.
// TOML and Lua are mutually exclusive: if config.lua exists, config.toml is ignored.
func LoadFromLua(path string) (Config, error) {
	base := Config{
		App: Application{
			MaxHTTPParallel:       100,
			GlobalConnectionLimit: 50,
			MaxRequestBodySize:    50 << 20,
		},
	}

	cfg, err := loadFromLua(path, base)
	if err != nil {
		return Config{}, err
	}

	applyDefaults(&cfg.App)
	return cfg, nil
}

func applyDefaults(app *Application) {
	if app.DownloadDir == "" {
		hd, err := os.UserHomeDir()
		if err != nil {
			panic(errgo.Wrap(err, "failed to get user homedir"))
		}
		app.DownloadDir = filepath.Join(hd, "downloads")
	}

	if app.GlobalUploadSlots == 0 {
		slots := max(app.GlobalConnectionLimit*4, 64)
		app.GlobalUploadSlots = slots
	}
}

// loadFromLua executes a Lua config script that receives a base config (from TOML + CLI)
// and may adjust values via neptune.set() / neptune.get().
func loadFromLua(path string, base Config) (Config, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return Config{}, errgo.Wrap(err, "read config script")
	}

	L := lua.NewState()
	defer L.Close()

	// Open base libraries: os, math, string, table (no io for safety).
	// We skip the package library to keep it simple.
	lua.OpenBase(L)
	lua.OpenMath(L)
	lua.OpenString(L)
	lua.OpenTable(L)

	// Open the standard os module, then extend it.
	lua.OpenOs(L)
	extendOS(L)

	// Register neptune module with set/get.
	registerNeptune(L, &base.App)

	// Register console.
	registerConsole(L)

	// Execute.
	if err := L.DoString(string(src)); err != nil {
		return Config{}, errgo.Wrap(err, "execute config script")
	}

	// Validate final config.
	validate := validator.New()
	if err := validate.Struct(base.App); err != nil {
		return Config{}, errgo.Wrap(err, "validate config after script")
	}

	return Config{App: base.App}, nil
}

// --- os module extensions ---

func extendOS(L *lua.LState) {
	t := L.GetGlobal("os").(*lua.LTable)

	t.RawSetString("getenv", L.NewFunction(luaOSGetenv))
	t.RawSetString("hostname", L.NewFunction(luaOSHostname))
	t.RawSetString("cpus", L.NewFunction(luaOSCpus))
}

func luaOSGetenv(L *lua.LState) int {
	name := L.CheckString(1)
	L.Push(lua.LString(os.Getenv(name)))
	return 1
}

func luaOSHostname(L *lua.LState) int {
	h, _ := os.Hostname()
	L.Push(lua.LString(h))
	return 1
}

func luaOSCpus(L *lua.LState) int {
	L.Push(lua.LNumber(runtime.NumCPU()))
	return 1
}

// --- neptune module ---

type configField struct {
	setter func(app *Application, v lua.LValue) error
	getter func(app *Application) lua.LValue
}

var configFields = map[string]configField{
	"application.download-dir": {
		setter: func(a *Application, v lua.LValue) error { a.DownloadDir = lua.LVAsString(v); return nil },
		getter: func(a *Application) lua.LValue { return lua.LString(a.DownloadDir) },
	},
	"application.max-http-parallel": {
		setter: func(a *Application, v lua.LValue) error {
			n, err := toGoInt(v)
			if err != nil {
				return err
			}
			a.MaxHTTPParallel = n
			return nil
		},
		getter: func(a *Application) lua.LValue { return lua.LNumber(a.MaxHTTPParallel) },
	},
	"application.p2p-port": {
		setter: func(a *Application, v lua.LValue) error {
			n, err := toGoUint16(v)
			if err != nil {
				return err
			}
			a.P2PPort = n
			return nil
		},
		getter: func(a *Application) lua.LValue { return lua.LNumber(a.P2PPort) },
	},
	"application.num-want": {
		setter: func(a *Application, v lua.LValue) error {
			n, err := toGoUint16(v)
			if err != nil {
				return err
			}
			a.NumWant = n
			return nil
		},
		getter: func(a *Application) lua.LValue { return lua.LNumber(a.NumWant) },
	},
	"application.global-connections-limit": {
		setter: func(a *Application, v lua.LValue) error {
			n, err := toGoUint16(v)
			if err != nil {
				return err
			}
			a.GlobalConnectionLimit = n
			return nil
		},
		getter: func(a *Application) lua.LValue { return lua.LNumber(a.GlobalConnectionLimit) },
	},
	"application.global-upload-slots": {
		setter: func(a *Application, v lua.LValue) error {
			n, err := toGoUint16(v)
			if err != nil {
				return err
			}
			a.GlobalUploadSlots = n
			return nil
		},
		getter: func(a *Application) lua.LValue { return lua.LNumber(a.GlobalUploadSlots) },
	},
	"application.global-download-speed-limit": {
		setter: func(a *Application, v lua.LValue) error {
			n, err := toGoInt64(v)
			if err != nil {
				return err
			}
			a.GlobalDownloadSpeedLimit = n
			return nil
		},
		getter: func(a *Application) lua.LValue { return lua.LNumber(a.GlobalDownloadSpeedLimit) },
	},
	"application.global-upload-speed-limit": {
		setter: func(a *Application, v lua.LValue) error {
			n, err := toGoInt64(v)
			if err != nil {
				return err
			}
			a.GlobalUploadSpeedLimit = n
			return nil
		},
		getter: func(a *Application) lua.LValue { return lua.LNumber(a.GlobalUploadSpeedLimit) },
	},
	"application.fallocate": {
		setter: func(a *Application, v lua.LValue) error { a.Fallocate = lua.LVAsBool(v); return nil },
		getter: func(a *Application) lua.LValue { return lua.LBool(a.Fallocate) },
	},
	"application.max-rpc-request-body-size": {
		setter: func(a *Application, v lua.LValue) error {
			n, err := toGoInt64(v)
			if err != nil {
				return err
			}
			a.MaxRequestBodySize = n
			return nil
		},
		getter: func(a *Application) lua.LValue { return lua.LNumber(a.MaxRequestBodySize) },
	},
	"application.piece-pick-strategy": {
		setter: func(a *Application, v lua.LValue) error {
			s := lua.LVAsString(v)
			if s != "rarest-first" && s != "sequential" {
				return fmt.Errorf("must be 'rarest-first' or 'sequential', got %q", s)
			}
			a.PiecePickStrategy = s
			return nil
		},
		getter: func(a *Application) lua.LValue { return lua.LString(a.PiecePickStrategy) },
	},
	"application.crypto": {
		setter: func(a *Application, v lua.LValue) error {
			s := lua.LVAsString(v)
			if _, err := ParseCryptoMode(s); err != nil {
				return err
			}
			a.Crypto = s
			return nil
		},
		getter: func(a *Application) lua.LValue { return lua.LString(a.Crypto) },
	},
}

func registerNeptune(L *lua.LState, app *Application) {
	neptune := L.NewTable()

	neptune.RawSetString("set", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		field, ok := configFields[key]
		if !ok {
			validKeys := make([]string, 0, len(configFields))
			for k := range configFields {
				validKeys = append(validKeys, k)
			}
			L.RaiseError("unknown config key %q, valid keys: %s", key, strings.Join(validKeys, ", "))
			return 0
		}
		val := L.Get(2)
		if err := field.setter(app, val); err != nil {
			L.RaiseError("invalid value for %q: %v", key, err)
		}
		return 0
	}))

	neptune.RawSetString("get", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		field, ok := configFields[key]
		if !ok {
			validKeys := make([]string, 0, len(configFields))
			for k := range configFields {
				validKeys = append(validKeys, k)
			}
			L.RaiseError("unknown config key %q, valid keys: %s", key, strings.Join(validKeys, ", "))
			return 0
		}
		L.Push(field.getter(app))
		return 1
	}))

	L.SetGlobal("neptune", neptune)
}

// --- console module ---

func registerConsole(L *lua.LState) {
	console := L.NewTable()

	console.RawSetString("log", L.NewFunction(func(L *lua.LState) int {
		top := L.GetTop()
		parts := make([]string, 0, top)
		for i := 1; i <= top; i++ {
			parts = append(parts, L.CheckAny(i).String())
		}
		_, _ = fmt.Fprintln(os.Stderr, "[config] "+strings.Join(parts, " "))
		return 0
	}))

	console.RawSetString("warn", L.NewFunction(func(L *lua.LState) int {
		top := L.GetTop()
		parts := make([]string, 0, top)
		for i := 1; i <= top; i++ {
			parts = append(parts, L.CheckAny(i).String())
		}
		_, _ = fmt.Fprintln(os.Stderr, "[config] WARN: "+strings.Join(parts, " "))
		return 0
	}))

	console.RawSetString("error", L.NewFunction(func(L *lua.LState) int {
		top := L.GetTop()
		parts := make([]string, 0, top)
		for i := 1; i <= top; i++ {
			parts = append(parts, L.CheckAny(i).String())
		}
		_, _ = fmt.Fprintln(os.Stderr, "[config] ERROR: "+strings.Join(parts, " "))
		return 0
	}))

	L.SetGlobal("console", console)
}

// --- helpers ---

func toGoInt(v lua.LValue) (int, error) {
	switch v.Type() {
	case lua.LTNumber:
		return int(lua.LVAsNumber(v)), nil
	case lua.LTString:
		var n int
		if _, err := fmt.Sscanf(lua.LVAsString(v), "%d", &n); err != nil {
			return 0, fmt.Errorf("cannot convert %q to integer", v.String())
		}
		return n, nil
	default:
		return 0, fmt.Errorf("expected number, got %s", v.Type())
	}
}

func toGoUint16(v lua.LValue) (uint16, error) {
	n, err := toGoInt(v)
	if err != nil {
		return 0, err
	}
	if n < 0 || n > 65535 {
		return 0, fmt.Errorf("value %d out of range for uint16", n)
	}
	return uint16(n), nil
}

func toGoInt64(v lua.LValue) (int64, error) {
	switch v.Type() {
	case lua.LTNumber:
		return int64(lua.LVAsNumber(v)), nil
	case lua.LTString:
		var n int64
		if _, err := fmt.Sscanf(lua.LVAsString(v), "%d", &n); err != nil {
			return 0, fmt.Errorf("cannot convert %q to integer", v.String())
		}
		return n, nil
	default:
		return 0, fmt.Errorf("expected number, got %s", v.Type())
	}
}
