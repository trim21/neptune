package mempool

import (
	"github.com/colega/zeropool"
	"github.com/docker/go-units"
	"github.com/valyala/bytebufferpool"
)

var pool = zeropool.New(func() []byte {
	return make([]byte, units.MiB)
})

func GetSlice() []byte {
	return pool.Get()
}

func PutSlice(slice []byte) {
	pool.Put(slice[:0])
}

func Get() *bytebufferpool.ByteBuffer {
	return bytebufferpool.Get()
}

func Put(b *bytebufferpool.ByteBuffer) {
	bytebufferpool.Put(b)
}
