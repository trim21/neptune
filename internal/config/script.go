// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"fmt"
	"maps"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/trim21/errgo"
	lua "github.com/yuin/gopher-lua"
)

// loadLua runs the Lua config script at path on top of cfg via the
// neptune.set() / neptune.get() API.
func loadLua(path string, cfg Config) (Config, error) {
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
	registerNeptune(L, &cfg)

	// Register console.
	registerConsole(L)

	// Execute.
	if err := L.DoString(string(src)); err != nil {
		return Config{}, errgo.Wrap(err, "execute config script")
	}

	return cfg, nil
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

var durationType = reflect.TypeFor[time.Duration]()

// configLeafFields is the leaf field set of the Config schema: every dotted
// toml tag path mapped to the field index path it addresses. Deriving it from
// the struct tags instead of writing it out by hand is what keeps the Lua key
// set in step with the TOML schema.
var configLeafFields = collectLeafFields(reflect.TypeFor[Config](), "", nil)

// collectLeafFields returns the leaf fields declared by t and its nested
// structs, with prefix prepended to each key and index to each field path.
func collectLeafFields(t reflect.Type, prefix string, index []int) map[string][]int {
	keys := make(map[string][]int)

	for i := range t.NumField() {
		field := t.Field(i)

		name, _, _ := strings.Cut(field.Tag.Get("toml"), ",")
		if name == "" || name == "-" {
			continue
		}

		path := slices.Concat(index, []int{i})
		key := prefix + name

		// Nested sections contribute their tag to the key path.
		if field.Type.Kind() == reflect.Struct {
			maps.Copy(keys, collectLeafFields(field.Type, key+".", path))
			continue
		}

		keys[key] = path
	}

	return keys
}

// validConfigKeys lists the known keys in a stable order for error messages.
func validConfigKeys() []string {
	keys := make([]string, 0, len(configLeafFields))
	for key := range configLeafFields {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// luaToFieldValue converts a Lua value into the Go type of a config field.
// Only conversion lives here: whether the converted value is acceptable is
// decided by Config.Validate(), which sees the config as a whole.
func luaToFieldValue(v lua.LValue, t reflect.Type) (reflect.Value, error) {
	if t == durationType {
		d, err := time.ParseDuration(lua.LVAsString(v))
		if err != nil {
			return reflect.Value{}, fmt.Errorf("invalid duration: %w", err)
		}
		return reflect.ValueOf(d), nil
	}

	switch t.Kind() {
	case reflect.String:
		return reflect.ValueOf(lua.LVAsString(v)).Convert(t), nil
	case reflect.Bool:
		return reflect.ValueOf(lua.LVAsBool(v)).Convert(t), nil
	case reflect.Int:
		n, err := toGoInt(v)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(n).Convert(t), nil
	case reflect.Int64:
		n, err := toGoInt64(v)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(n).Convert(t), nil
	case reflect.Uint16:
		n, err := toGoUint16(v)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(n).Convert(t), nil
	default:
		return reflect.Value{}, fmt.Errorf("unsupported config field type %s", t)
	}
}

func fieldValueToLua(v reflect.Value) lua.LValue {
	if v.Type() == durationType {
		return lua.LString(time.Duration(v.Int()).String())
	}

	switch v.Kind() {
	case reflect.String:
		return lua.LString(v.String())
	case reflect.Bool:
		return lua.LBool(v.Bool())
	case reflect.Int, reflect.Int64:
		return lua.LNumber(v.Int())
	case reflect.Uint16:
		return lua.LNumber(v.Uint())
	default:
		panic("unsupported config field type " + v.Type().String())
	}
}

func registerNeptune(L *lua.LState, cfg *Config) {
	neptune := L.NewTable()

	neptune.RawSetString("set", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		index, ok := configLeafFields[key]
		if !ok {
			L.RaiseError("unknown config key %q, valid keys: %s", key, strings.Join(validConfigKeys(), ", "))
			return 0
		}

		target := reflect.ValueOf(cfg).Elem().FieldByIndex(index)

		value, err := luaToFieldValue(L.Get(2), target.Type())
		if err != nil {
			L.RaiseError("invalid value for %q: %v", key, err)
			return 0
		}

		target.Set(value)
		return 0
	}))

	neptune.RawSetString("get", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		index, ok := configLeafFields[key]
		if !ok {
			L.RaiseError("unknown config key %q, valid keys: %s", key, strings.Join(validConfigKeys(), ", "))
			return 0
		}

		L.Push(fieldValueToLua(reflect.ValueOf(cfg).Elem().FieldByIndex(index)))
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
