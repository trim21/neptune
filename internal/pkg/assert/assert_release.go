//go:build release

package assert

func Equal[T comparable](v1, v2 T, msg ...string)    {}
func NotEqual[T comparable](v1, v2 T, msg ...string) {}
