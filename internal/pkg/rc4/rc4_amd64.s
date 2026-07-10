// Original source:
//  http://www.zorinaq.com/papers/rc4-amd64.html
//  http://www.zorinaq.com/papers/rc4-amd64.tar.bz2
//
// Transliterated from GNU to Go asm syntax by the Go authors.
// Adapted for Neptune from Go's crypto/rc4 (commit 75d15a2082^).

#include "textflag.h"

// On some CPUs, zero-extending a byte register into the full 64-bit register
// avoids a stall after 8-bit math. On others (e.g. Core i5), it's a no-op
// via register renaming but makes the code slower on Xeon.
#define EXTEND(r) MOVBLZX r, r

// func xorKeyStream(dst, src *byte, n int, state *[256]uint32, i, j *uint8)
TEXT ·xorKeyStream(SB),NOSPLIT,$0
	MOVQ	n+16(FP),	BX		// rbx = ARG(n)
	MOVQ	src+8(FP),	SI		// in = ARG(src)
	MOVQ	dst+0(FP),	DI		// out = ARG(dst)
	MOVQ	state+24(FP),	BP		// d = ARG(state)
	MOVQ	i+32(FP),	AX
	MOVBQZX	0(AX),		CX		// x = *xp
	MOVQ	j+40(FP),	AX
	MOVBQZX	0(AX),		DX		// y = *yp

	LEAQ	(SI)(BX*1),	R9		// limit = in+len

l1:	CMPQ	SI,		R9		// cmp in with in+len
	JGE	finished			// jump if (in >= in+len)

	INCB	CX
	EXTEND(CX)
	TESTL	$15,		CX
	JZ	wordloop

	MOVBLZX	(BP)(CX*4),	AX

	ADDB	AX,		DX		// y += tx
	EXTEND(DX)
	MOVBLZX	(BP)(DX*4),	BX		// ty = d[y]
	MOVB	BX,		(BP)(CX*4)	// d[x] = ty
	ADDB	AX,		BX		// val = ty+tx
	EXTEND(BX)
	MOVB	AX,		(BP)(DX*4)	// d[y] = tx
	MOVBLZX	(BP)(BX*4),	R8		// val = d[val]
	XORB	(SI),		R8		// xor 1 byte
	MOVB	R8,		(DI)
	INCQ	SI				// in++
	INCQ	DI				// out++
	JMP l1

wordloop:
	SUBQ	$16,		R9
	CMPQ	SI,		R9
	JGT	end

start:
	ADDQ	$16,		SI		// increment in
	ADDQ	$16,		DI		// increment out

	// Each KEYROUND generates one byte of key and inserts it into
	// an XMM register at the given 16-bit index.
	// The key state array is uint32 words; only the bottom byte is used,
	// so the 16-bit OR only copies 8 useful bits.
	// Alternating bytes go into X0 and X1; at the end,
	// PSLLQ $8; PXOR merges X1 into X0 for the full 128-bit keystream.
	//
	// At loop entry, CX%16 == 0, so state[CX..CX+15] fits without wrap.
	// R12 = state + CX*4 precomputed once for all 16 accesses.
	LEAQ	(BP)(CX*4),	R12

#define KEYROUND(xmm, load, off, r1, r2, index) \
	MOVBLZX	(BP)(DX*4),	R8; \
	MOVB	r1,		(BP)(DX*4); \
	load((off+1), r2); \
	MOVB	R8,		(off*4)(R12); \
	ADDB	r1,		R8; \
	EXTEND(R8); \
	PINSRW	$index, (BP)(R8*4), xmm

#define LOAD(off, reg) \
	MOVBLZX	(off*4)(R12),	reg; \
	ADDB	reg,		DX; \
	EXTEND(DX)

#define SKIP(off, reg)

	LOAD(0, AX)
	KEYROUND(X0, LOAD, 0, AX, BX, 0)
	KEYROUND(X1, LOAD, 1, BX, AX, 0)
	KEYROUND(X0, LOAD, 2, AX, BX, 1)
	KEYROUND(X1, LOAD, 3, BX, AX, 1)
	KEYROUND(X0, LOAD, 4, AX, BX, 2)
	KEYROUND(X1, LOAD, 5, BX, AX, 2)
	KEYROUND(X0, LOAD, 6, AX, BX, 3)
	KEYROUND(X1, LOAD, 7, BX, AX, 3)
	KEYROUND(X0, LOAD, 8, AX, BX, 4)
	KEYROUND(X1, LOAD, 9, BX, AX, 4)
	KEYROUND(X0, LOAD, 10, AX, BX, 5)
	KEYROUND(X1, LOAD, 11, BX, AX, 5)
	KEYROUND(X0, LOAD, 12, AX, BX, 6)
	KEYROUND(X1, LOAD, 13, BX, AX, 6)
	KEYROUND(X0, LOAD, 14, AX, BX, 7)
	KEYROUND(X1, SKIP, 15, BX, AX, 7)
	
	ADDB	$16,		CX

	PSLLQ	$8,		X1
	PXOR	X1,		X0
	MOVOU	-16(SI),	X2
	PXOR	X0,		X2
	MOVOU	X2,		-16(DI)

	CMPQ	SI,		R9		// cmp in with in+len-16
	JLE	start				// jump if (in <= in+len-16)

end:
	DECB	CX
	ADDQ	$16,		R9		// tmp = in+len

	// handle the last bytes, one by one
l2:	CMPQ	SI,		R9		// cmp in with in+len
	JGE	finished			// jump if (in >= in+len)

	INCB	CX
	EXTEND(CX)
	MOVBLZX	(BP)(CX*4),	AX

	ADDB	AX,		DX		// y += tx
	EXTEND(DX)
	MOVBLZX	(BP)(DX*4),	BX		// ty = d[y]
	MOVB	BX,		(BP)(CX*4)	// d[x] = ty
	ADDB	AX,		BX		// val = ty+tx
	EXTEND(BX)
	MOVB	AX,		(BP)(DX*4)	// d[y] = tx
	MOVBLZX	(BP)(BX*4),	R8		// val = d[val]
	XORB	(SI),		R8		// xor 1 byte
	MOVB	R8,		(DI)
	INCQ	SI				// in++
	INCQ	DI				// out++
	JMP l2

finished:
	MOVQ	j+40(FP),	BX
	MOVB	DX, 0(BX)
	MOVQ	i+32(FP),	AX
	MOVB	CX, 0(AX)
	RET
