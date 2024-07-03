//go:build !release

// development helper package
// check int overflow in non release mode

package as

import (
	"fmt"
	"math"
	"reflect"
)

var rU8 = reflect.ValueOf(uint8(0))
var rU16 = reflect.ValueOf(uint16(0))
var rU32 = reflect.ValueOf(uint32(0))
var rU64 = reflect.ValueOf(uint64(0))
var rI8 = reflect.ValueOf(int8(0))
var rI16 = reflect.ValueOf(int16(0))
var rI32 = reflect.ValueOf(int32(0))
var rI64 = reflect.ValueOf(int64(0))
var rI = reflect.ValueOf(int(0))

func Uint8[T int8 | int16 | int32 | int64 | int | uint16 | uint32 | uint64 | uint](v T) uint8 {
	rv := reflect.ValueOf(v)
	if rv.CanUint() {
		if rU8.OverflowUint(rv.Uint()) {
			panic(fmt.Sprintf("%d overflow uint8", v))
		}
	} else if rv.Int() < 0 || rv.Int() > math.MaxUint8 {
		panic(fmt.Sprintf("%d overflow uint8", v))
	}

	return uint8(v)
}

func Uint16[T int8 | int16 | int32 | int64 | int | uint8 | uint32 | uint64 | uint](v T) uint16 {
	rv := reflect.ValueOf(v)
	if rv.CanUint() {
		if rU16.OverflowUint(rv.Uint()) {
			panic(fmt.Sprintf("%d overflow uint16", v))
		}
	} else if rv.Int() < 0 || rv.Int() > math.MaxUint16 {
		panic(fmt.Sprintf("%d overflow uint16", v))
	}

	return uint16(v)
}

func Uint32[T int8 | int16 | int32 | int64 | int | uint8 | uint16 | uint64 | uint](v T) uint32 {
	rv := reflect.ValueOf(v)
	if rv.CanUint() {
		if rU32.OverflowUint(rv.Uint()) {
			panic(fmt.Sprintf("%d overflow uint32", v))
		}
	} else if rv.Int() < 0 || rv.Int() > math.MaxUint32 {
		panic(fmt.Sprintf("%d overflow uint32", v))
	}

	return uint32(v)
}

func Uint64[T int8 | int16 | int32 | int64 | int | uint8 | uint16 | uint32 | uint](v T) uint64 {
	rv := reflect.ValueOf(v)
	if rv.CanInt() && rv.Int() < 0 {
		panic(fmt.Sprintf("%d overflow uint64", v))
	}

	return uint64(v)
}

func Int8[T int16 | int32 | int64 | int | uint8 | uint16 | uint32 | uint64 | uint](v T) int8 {
	rv := reflect.ValueOf(v)
	if rv.CanUint() {
		if rv.Uint() > math.MaxInt8 {
			panic(fmt.Sprintf("%d overflow int8", v))
		}
	} else if rv.CanInt() {
		if rI8.OverflowInt(rv.Int()) {
			panic(fmt.Sprintf("%d overflow int8", v))
		}
	}

	return int8(v)
}

func Int16[T int8 | int32 | int64 | int | uint8 | uint16 | uint32 | uint64 | uint](v T) int16 {
	rv := reflect.ValueOf(v)
	if rv.CanUint() {
		if rv.Uint() > math.MaxInt16 {
			panic(fmt.Sprintf("%d overflow int16", v))
		}
	} else if rv.CanInt() {
		if rI16.OverflowInt(rv.Int()) {
			panic(fmt.Sprintf("%d overflow int16", v))
		}
	}

	return int16(v)
}

func Int32[T int8 | int16 | int64 | int | uint8 | uint16 | uint32 | uint64 | uint](v T) int32 {
	rv := reflect.ValueOf(v)
	if rv.CanUint() {
		if rv.Uint() > math.MaxInt32 {
			panic(fmt.Sprintf("%d overflow int32", v))
		}
	} else if rv.CanInt() {
		if rI32.OverflowInt(rv.Int()) {
			panic(fmt.Sprintf("%d overflow int32", v))
		}
	}

	return int32(v)
}

func Int64[T int8 | int16 | int32 | int | uint8 | uint16 | uint32 | uint64 | uint](v T) int64 {
	rv := reflect.ValueOf(v)
	if rv.CanUint() {
		if rv.Uint() > math.MaxInt64 {
			panic(fmt.Sprintf("%d overflow int64", v))
		}
	}

	return int64(v)
}

func Int[T int8 | int16 | int32 | int64 | uint8 | uint16 | uint32 | uint64 | uint](v T) int {
	rv := reflect.ValueOf(v)
	if rv.CanUint() {
		if rv.Uint() > math.MaxInt {
			panic(fmt.Sprintf("%d overflow int", v))
		}
	} else if rv.CanInt() {
		if rI.OverflowInt(rv.Int()) {
			panic(fmt.Sprintf("%d overflow int", v))
		}
	}

	return int(v)
}
