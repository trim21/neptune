// Copyright 2024 trim21 <trim21.me@gmail.com>
// Copyright https://github.com/anacrolix
// SPDX-License-Identifier: MPL-2.0
// https://github.com/anacrolix/torrent/blob/v1.56.1/LICENSE

package mse

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/rc4"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"

	"github.com/go-faster/xor"
	"golang.org/x/sync/errgroup"

	"tyr/internal/pkg/gsync"
	"tyr/internal/pkg/mempool"
)

type CryptoMethod uint32

const (
	CryptoMethodPlaintext CryptoMethod = 1 // After header obfuscation, drop into plaintext
	CryptoMethodRC4       CryptoMethod = 2 // After header obfuscation, use RC4 for the rest of the stream
)

//var cryptoProvidesCount = expvar.NewMap("mseCryptoProvides")

const AllSupportedCrypto = CryptoMethodPlaintext | CryptoMethodRC4

var (
	// Prime P according to the spec, and G, the generator.
	primeP, primeG big.Int
	// For use in initer's hashes
	req1 = []byte("req1")
	req2 = []byte("req2")
	req3 = []byte("req3")
	// Verification constant "VC" which is all zeroes in the bittorrent
	// implementation.
	vc [8]byte
	// Zero padding
	zeroPad [512]byte
	// Tracks counts of received crypto_provides
)

func init() {
	primeP.SetString("0xFFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A63A36210000000000090563", 0)
	primeG.SetInt64(2)
}

const maxPadLength = 512

var pool = gsync.NewPool(
	func() *mempool.Buffer {
		return &mempool.Buffer{
			B: make([]byte, maxPadLength),
		}
	},
)

func hashBytes(parts ...[]byte) []byte {
	//h := shaPool.Get()
	//defer shaPool.Put(h)
	//h.Reset()
	h := sha1.New()
	for _, p := range parts {
		// it will never fail
		_, _ = h.Write(p)
	}
	return h.Sum(nil)
}

func newEncrypt(initer bool, s, skey []byte) (c *rc4.Cipher) {
	// will never fail if we have valid key length
	if initer {
		c, _ = rc4.NewCipher(hashBytes([]byte("keyA"), s, skey))
	} else {
		c, _ = rc4.NewCipher(hashBytes([]byte("keyB"), s, skey))
	}

	var burnSrc, burnDst [1024]byte

	c.XORKeyStream(burnDst[:], burnSrc[:])

	return
}

type cipherReader struct {
	c  *rc4.Cipher
	r  io.Reader
	be []byte
}

func (cr *cipherReader) Read(b []byte) (n int, err error) {
	if cap(cr.be) < len(b) {
		cr.be = make([]byte, len(b))
	}
	n, err = cr.r.Read(cr.be[:len(b)])
	cr.c.XORKeyStream(b[:n], cr.be[:n])
	return
}

func newCipherReader(c *rc4.Cipher, r io.Reader) io.Reader {
	return &cipherReader{c: c, r: r}
}

type cipherWriter struct {
	c *rc4.Cipher
	w io.Writer
	b []byte
}

func (cr *cipherWriter) Write(b []byte) (n int, err error) {
	var be []byte

	if len(cr.b) < len(b) {
		be = make([]byte, len(b))
	} else {
		be = cr.b
		cr.b = nil
	}

	cr.c.XORKeyStream(be, b)
	n, err = cr.w.Write(be[:len(b)])
	if n != len(b) {
		// The cipher will have advanced beyond the callers stream position.
		// We can't use the cipher anymore.
		cr.c = nil
	}

	cr.b = be

	return
}

var intPool = gsync.NewPool(func() *big.Int {
	return &big.Int{}
})

func newX() *big.Int {
	b := pool.Get()
	defer pool.Put(b)

	buf := b.B[:20]

	_, err := rand.Read(buf)
	if err != nil {
		panic(fmt.Errorf("crypto/rand.Read: %v", err))
	}

	x := intPool.Get()

	x.SetBytes(buf)

	return x
}

// Calculate, and send Y, our public key.
func (h *handshake) postY(x *big.Int) error {
	var y = intPool.Get()
	defer intPool.Put(y)
	y.Exp(&primeG, x, &primeP)

	buf := pool.Get()
	defer pool.Put(buf)

	y.FillBytes(buf.B[:96])
	return h.write(buf.B[:96])
}

func (h *handshake) establish() error {
	x := newX()
	defer intPool.Put(x)

	var group errgroup.Group

	group.Go(func() error {
		if err := h.postY(x); err != nil {
			return err
		}
		return h.w.Flush()
	})

	group.Go(func() error {
		_, err := io.ReadFull(h.conn, h.s[:])
		if err != nil {
			return fmt.Errorf("error reading Y: %w", err)
		}

		Y := intPool.Get()
		defer intPool.Put(Y)

		S := intPool.Get()
		defer intPool.Put(S)

		Y.SetBytes(h.s[:])

		S.Exp(Y, x, &primeP)

		S.FillBytes(h.s[:])

		return nil
	})

	return group.Wait()
}

func newPadLen() uint16 {
	var b [2]byte

	_, err := rand.Read(b[:])
	if err != nil {
		panic(fmt.Sprintln("unexpected error when reading from random", err))
	}

	// [0-65535] % 512 has no bias
	return binary.BigEndian.Uint16(b[:]) % maxPadLength
}

// Manages state for both initiating and receiving handshakes.
type handshake struct {
	conn  io.ReadWriter
	w     *bufio.Writer
	skeys SecretKeyIter // Skeys we'll accept if receiving.
	// Return the bit for the crypto method the receiver wants to use.
	chooseMethod CryptoSelector
	skey         []byte // Skey we're initiating with.
	ia           []byte // Initial payload. Only used by the initiator.
	// Sent to the receiver.
	cryptoProvides CryptoMethod
	s              [96]byte
	initer         bool // Whether we're initiating or receiving.
}

func (h *handshake) write(b []byte) error {
	_, err := h.w.Write(b)
	return err
}

func xorToBytes(a, b []byte) (ret []byte) {
	if len(a) != len(b) {
		panic(fmt.Sprintf("len(a) != len(b), %d vs %d", len(a), len(b)))
	}
	ret = make([]byte, len(a))
	xor.Bytes(ret, a, b)
	return
}

func marshal(w io.Writer, data ...any) (err error) {
	for _, data := range data {
		err = binary.Write(w, binary.BigEndian, data)
		if err != nil {
			break
		}
	}
	return
}

func unmarshal(r io.Reader, data ...any) (err error) {
	for _, data := range data {
		err = binary.Read(r, binary.BigEndian, data)
		if err != nil {
			break
		}
	}
	return
}

// Looking for b at the end of a.
func suffixMatchLen(a, b []byte) int {
	if len(b) > len(a) {
		b = b[:len(a)]
	}
	// i is how much of b to try to match
	for i := len(b); i > 0; i-- {
		// j is how many chars we've compared
		j := 0
		for ; j < i; j++ {
			if b[i-1-j] != a[len(a)-1-j] {
				goto shorter
			}
		}
		return j
	shorter:
	}
	return 0
}

// Reads from r until b has been seen. Keeps the minimum amount of data in
// memory.
func readUntil(r io.Reader, b []byte) error {
	b1 := make([]byte, len(b))
	i := 0
	for {
		_, err := io.ReadFull(r, b1[i:])
		if err != nil {
			return err
		}
		i = suffixMatchLen(b1, b)
		if i == len(b) {
			break
		}
		if copy(b1, b1[len(b1)-i:]) != i {
			panic("wat")
		}
	}
	return nil
}

type readWriter struct {
	io.Reader
	io.Writer
}

func (h *handshake) newEncrypt(initer bool) *rc4.Cipher {
	return newEncrypt(initer, h.s[:], h.skey)
}

var errInitialPayloadTooLarge = errors.New("initial payload too large")

func (h *handshake) initerSteps() (ret io.ReadWriter, selected CryptoMethod, err error) {
	var g errgroup.Group
	e := h.newEncrypt(true)

	// write
	g.Go(func() error {
		err := h.write(hashBytes(req1, h.s[:]))
		if err != nil {
			return err
		}

		var padLen uint16
		padLen = 0
		//padLen := newPadLen()

		err = h.write(xorToBytes(hashBytes(req2, h.skey), hashBytes(req3, h.s[:])))
		if err != nil {
			return err
		}

		if len(h.ia) > math.MaxUint16 {
			return errInitialPayloadTooLarge
		}

		buf := mempool.Get()
		defer mempool.Put(buf)

		err = marshal(buf, vc[:], h.cryptoProvides, padLen)
		if err != nil {
			return err
		}

		if padLen != 0 {
			padBuf := mempool.Get()
			defer mempool.Put(padBuf)
			_, errR := io.CopyBuffer(buf, io.LimitReader(rand.Reader, int64(padLen)), padBuf.B[:maxPadLength])
			if errR != nil {
				panic(fmt.Sprintln("error reading from random", err))
			}
		}

		err = marshal(buf, uint16(len(h.ia)), h.ia)
		if err != nil {
			return err
		}

		be := mempool.GetWithCap(buf.Len())
		defer mempool.Put(be)

		e.XORKeyStream(be.B[:buf.Len()], buf.Bytes())
		err = h.write(be.B[:buf.Len()])
		if err != nil {
			return err
		}

		return h.w.Flush()
	})

	var method CryptoMethod

	var r io.Reader

	// read
	g.Go(func() error {
		var eVC [8]byte
		bC := h.newEncrypt(false)
		bC.XORKeyStream(eVC[:], vc[:])
		// Read until the all zero VC. At this point we've only read the 96 byte
		// public key, Y. There is potentially 512 byte padding, between us and
		// the 8 byte verification constant.
		err = readUntil(io.LimitReader(h.conn, 520), eVC[:])
		if err != nil {
			if err == io.EOF {
				return errors.New("failed to synchronize on VC")
			}
			return fmt.Errorf("error reading until VC: %s", err)
		}

		r = newCipherReader(bC, h.conn)

		var padLen uint16
		err = unmarshal(r, &method, &padLen)
		if err != nil {
			return err
		}
		_, err = io.CopyN(io.Discard, r, int64(padLen))
		if err != nil {
			return err
		}

		return nil
	})

	err = g.Wait()
	if err != nil {
		return
	}

	selected = method & h.cryptoProvides
	switch selected { //nolint:exhaustive
	case CryptoMethodRC4:
		ret = readWriter{r, &cipherWriter{e, h.conn, nil}}
	case CryptoMethodPlaintext:
		ret = h.conn
	default:
		err = fmt.Errorf("receiver chose unsupported method: %x", method)
	}
	return
}

var ErrNoSecretKeyMatch = errors.New("no skey matched")

func (h *handshake) receiverSteps() (ret io.ReadWriter, chosen CryptoMethod, err error) {
	// There is up to 512 bytes of padding, then the 20 byte hashBytes.
	err = readUntil(io.LimitReader(h.conn, 532), hashBytes(req1, h.s[:]))
	if err != nil {
		if err == io.EOF {
			err = errors.New("failed to synchronize on S hashBytes")
		}
		return
	}

	var b [20]byte
	_, err = io.ReadFull(h.conn, b[:])
	if err != nil {
		return
	}
	expectedHash := hashBytes(req3, h.s[:])
	eachHash := sha1.New()
	var sum, xored [sha1.Size]byte
	err = ErrNoSecretKeyMatch
	h.skeys(func(skey []byte) bool {
		eachHash.Reset()
		eachHash.Write(req2)
		eachHash.Write(skey)
		eachHash.Sum(sum[:0])
		xor.Bytes(xored[:], sum[:], expectedHash)
		if bytes.Equal(xored[:], b[:]) {
			h.skey = skey
			err = nil
			return false
		}
		return true
	})
	if err != nil {
		return
	}

	r := newCipherReader(newEncrypt(true, h.s[:], h.skey), h.conn)
	var (
		vc       [8]byte
		provides CryptoMethod
		padLen   uint16
	)

	err = unmarshal(r, vc[:], &provides, &padLen)
	if err != nil {
		return
	}
	chosen = h.chooseMethod(provides)
	_, err = io.CopyN(io.Discard, r, int64(padLen))
	if err != nil {
		return
	}
	var lenIA uint16
	if err = unmarshal(r, &lenIA); err != nil {
		return
	}

	if lenIA != 0 {
		h.ia = make([]byte, lenIA)
		err = unmarshal(r, h.ia)
		if err != nil {
			return
		}
	}

	buf := mempool.Get()
	defer mempool.Put(buf)

	w := cipherWriter{h.newEncrypt(false), buf, nil}

	padLen = newPadLen()
	err = marshal(&w, &vc, uint32(chosen), padLen, zeroPad[:padLen])
	if err != nil {
		return
	}

	if err = h.write(buf.Bytes()); err != nil {
		return
	}

	if err = h.w.Flush(); err != nil {
		return
	}

	switch chosen { //nolint:exhaustive
	case CryptoMethodRC4:
		ret = readWriter{
			io.MultiReader(bytes.NewReader(h.ia), r),
			&cipherWriter{w.c, h.conn, nil},
		}
	case CryptoMethodPlaintext:
		ret = readWriter{
			io.MultiReader(bytes.NewReader(h.ia), h.conn),
			h.conn,
		}
	default:
		err = errors.New("chosen crypto method is not supported")
	}

	return
}

// Do https://github.com/transmission/transmission/blob/7f79cb16ee194d58ce665f9319524bc5e6e4f91d/extras/encryption.txt#L136-L140
func (h *handshake) Do() (rw io.ReadWriter, method CryptoMethod, err error) {
	err = h.establish()
	if err != nil {
		err = fmt.Errorf("error while establishing secret: %w", err)
		return
	}

	{ // io.CopyN(h.w, rand.Reader, int64(size)) but zero alloc
		pad := pool.Get()
		defer pool.Put(pad)

		size := newPadLen()

		_, err = io.ReadFull(rand.Reader, pad.B[:size])
		if err != nil {
			panic(fmt.Sprintln("unexpected error when reading from random", err))
		}

		err = h.write(pad.B[:size])
		if err != nil {
			return
		}
	}

	if h.initer {
		return h.initerSteps()
	}

	return h.receiverSteps()
}

var bufioPool = gsync.NewPool(func() *bufio.Writer {
	return bufio.NewWriter(nil)
})

func InitiateHandshake(rw io.ReadWriter, key, initialPayload []byte, cryptoProvides CryptoMethod) (
	io.ReadWriter, CryptoMethod, error,
) {

	w := bufioPool.Get()
	defer bufioPool.Put(w)
	w.Reset(rw)

	h := handshake{
		conn:           rw,
		w:              w,
		initer:         true,
		skey:           key,
		ia:             initialPayload,
		cryptoProvides: cryptoProvides,
	}

	return h.Do()
}

func ReceiveHandshake(rw io.ReadWriter, keys SecretKeyIter, selectCrypto CryptoSelector) (
	io.ReadWriter, CryptoMethod, error,
) {
	w := bufioPool.Get()
	defer bufioPool.Put(w)
	w.Reset(rw)

	h := handshake{
		conn:         rw,
		w:            w,
		initer:       false,
		skeys:        keys,
		chooseMethod: selectCrypto,
	}

	return h.Do()
}

func DefaultCryptoSelector(provided CryptoMethod) CryptoMethod {
	// We prefer plaintext for performance reasons.
	if provided&CryptoMethodPlaintext != 0 {
		return CryptoMethodPlaintext
	}
	return CryptoMethodRC4
}

type SecretKeyIter func(callback func(skey []byte) (more bool))

type CryptoSelector func(CryptoMethod) CryptoMethod
