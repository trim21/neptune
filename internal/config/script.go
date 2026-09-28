// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strconv"
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
// Every type accepts its own Lua type and a string, which is parsed as the
// target type; nothing else is converted, so a value can never be silently
// rounded, truncated or emptied.
//
// Only conversion lives here: whether the converted value is acceptable is
// decided by Config.Validate(), which sees the config as a whole.
func luaToFieldValue(v lua.LValue, t reflect.Type) (reflect.Value, error) {
	if t == durationType {
		return durationValue(v)
	}

	switch t.Kind() {
	case reflect.String:
		return stringValue(v)
	case reflect.Bool:
		return boolValue(v)
	case reflect.Int:
		return intValue(v)
	case reflect.Int64:
		return int64Value(v)
	case reflect.Uint16:
		return uint16Value(v)
	default:
		return reflect.Value{}, fmt.Errorf("unsupported config field type %s", t)
	}
}

func stringValue(v lua.LValue) (reflect.Value, error) {
	s, ok := v.(lua.LString)
	if !ok {
		return reflect.Value{}, unexpectedType("string", v)
	}

	return reflect.ValueOf(string(s)), nil
}

func boolValue(v lua.LValue) (reflect.Value, error) {
	switch value := v.(type) {
	case lua.LBool:
		return reflect.ValueOf(bool(value)), nil
	case lua.LString:
		switch string(value) {
		case "true":
			return reflect.ValueOf(true), nil
		case "false":
			return reflect.ValueOf(false), nil
		default:
			return reflect.Value{}, fmt.Errorf(`expected "true" or "false", got %q`, string(value))
		}
	default:
		return reflect.Value{}, unexpectedType("boolean or string", v)
	}
}

func durationValue(v lua.LValue) (reflect.Value, error) {
	s, ok := v.(lua.LString)
	if !ok {
		return reflect.Value{}, unexpectedType("string", v)
	}

	d, err := time.ParseDuration(string(s))
	if err != nil {
		return reflect.Value{}, errgo.Wrap(err, "invalid duration")
	}

	return reflect.ValueOf(d), nil
}

func intValue(v lua.LValue) (reflect.Value, error) {
	switch value := v.(type) {
	case lua.LNumber:
		n, err := numberToInt(float64(value))
		if err != nil {
			return reflect.Value{}, err
		}

		return reflect.ValueOf(n), nil
	case lua.LString:
		n, err := strconv.ParseInt(string(value), 10, strconv.IntSize)
		if err != nil {
			return reflect.Value{}, integerTextError(string(value), "int", err)
		}

		return reflect.ValueOf(int(n)), nil
	default:
		return reflect.Value{}, unexpectedType("number or string", v)
	}
}

func int64Value(v lua.LValue) (reflect.Value, error) {
	switch value := v.(type) {
	case lua.LNumber:
		n, err := numberToInt64(float64(value))
		if err != nil {
			return reflect.Value{}, err
		}

		return reflect.ValueOf(n), nil
	case lua.LString:
		n, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil {
			return reflect.Value{}, integerTextError(string(value), "int64", err)
		}

		return reflect.ValueOf(n), nil
	default:
		return reflect.Value{}, unexpectedType("number or string", v)
	}
}

func uint16Value(v lua.LValue) (reflect.Value, error) {
	switch value := v.(type) {
	case lua.LNumber:
		n, err := numberToUint16(float64(value))
		if err != nil {
			return reflect.Value{}, err
		}

		return reflect.ValueOf(n), nil
	case lua.LString:
		n, err := strconv.ParseUint(string(value), 10, 16)
		if err != nil {
			return reflect.Value{}, integerTextError(string(value), "uint16", err)
		}

		return reflect.ValueOf(uint16(n)), nil
	default:
		return reflect.Value{}, unexpectedType("number or string", v)
	}
}

func numberToInt(f float64) (int, error) {
	n, err := numberToInt64(f)
	if err != nil {
		return 0, err
	}

	// int is 32 bits wide on some targets.
	if int64(int(n)) != n {
		return 0, fmt.Errorf("value %v out of range for int", f)
	}

	return int(n), nil
}

func numberToInt64(f float64) (int64, error) {
	if err := checkWhole(f); err != nil {
		return 0, err
	}

	// MaxInt64 has no float64 representation, so the bound compared against is
	// the power of two above it, which int64 cannot hold either.
	if f < math.MinInt64 || f >= math.MaxInt64 {
		return 0, fmt.Errorf("value %v out of range for int64", f)
	}

	return int64(f), nil
}

func numberToUint16(f float64) (uint16, error) {
	if err := checkWhole(f); err != nil {
		return 0, err
	}

	if f < 0 || f > math.MaxUint16 {
		return 0, fmt.Errorf("value %v out of range for uint16", f)
	}

	return uint16(f), nil
}

// checkWhole rejects a fractional number. Lua has a single number type, so an
// integer field states its requirement here rather than relying on a cast.
func checkWhole(f float64) error {
	if f != math.Trunc(f) {
		return fmt.Errorf("expected an integer, got %v", f)
	}

	return nil
}

// integerTextError describes what is wrong with a decimal string without
// repeating the parsing function's name back at the user.
func integerTextError(text, typ string, err error) error {
	if errors.Is(err, strconv.ErrRange) {
		return fmt.Errorf("value %s out of range for %s", text, typ)
	}

	return fmt.Errorf("%q is not an integer", text)
}

func unexpectedType(expected string, v lua.LValue) error {
	return fmt.Errorf("expected %s, got %s", expected, v.Type())
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
