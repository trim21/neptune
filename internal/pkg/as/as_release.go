//go:build release

package as

func Uint8[T int8 | int16 | int32 | int64 | int | uint16 | uint32 | uint64 | uint](v T) uint8 {
	return uint8(v)
}

func Uint16[T int8 | int16 | int32 | int64 | int | uint8 | uint32 | uint64 | uint](v T) uint16 {
	return uint16(v)
}

func Uint32[T int8 | int16 | int32 | int64 | int | uint8 | uint16 | uint64 | uint](v T) uint32 {
	return uint32(v)
}

func Uint64[T int8 | int16 | int32 | int64 | int | uint8 | uint16 | uint32 | uint](v T) uint64 {
	return uint64(v)
}

func Int8[T int16 | int32 | int64 | int | uint8 | uint16 | uint32 | uint64 | uint](v T) int8 {
	return int8(v)
}

func Int16[T int8 | int32 | int64 | int | uint8 | uint16 | uint32 | uint64 | uint](v T) int16 {
	return int16(v)
}

func Int32[T int8 | int16 | int64 | int | uint8 | uint16 | uint32 | uint64 | uint](v T) int32 {
	return int32(v)
}

func Int64[T int8 | int16 | int32 | int | uint8 | uint16 | uint32 | uint64 | uint](v T) int64 {
	return int64(v)
}

func Int[T int8 | int16 | int32 | int64 | uint8 | uint16 | uint32 | uint64 | uint](v T) int {
	return int(v)
}
