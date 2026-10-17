// Ported from Aspeed's decoder_js.js and yuv2rgb.js (Copyright (C) 2021 Aspeed Tech., MPL 2.0).
//
// The port is deliberately literal so its output matches the reference bit for bit, including
// JavaScript semantics the reference relies on: shift counts masked to 5 bits, typed arrays that
// wrap (Int16Array, Uint32Array) and silently drop out-of-range writes, NaN from out-of-range
// reads ending up as 0, and float64 colour conversion with Math.floor.

package aspeed

import (
	"encoding/binary"
	"errors"
	"math"
)

// Header describes one ASPEED frame.
type Header struct {
	Width, Height   int
	Mode420         byte // 1: YUV 4:2:0 in 16x16 macroblocks, 0: YUV 4:4:4 in 8x8 blocks
	LumaSelector    byte // quantisation table for Y
	ChromaSelector  byte // quantisation table for Cb/Cr
	AdvanceSelector byte // tables for second-pass ("advance") blocks
}

// ErrShortBitstream reports a bitstream too short to hold the two initial 32-bit words.
var ErrShortBitstream = errors.New("aspeed: bitstream shorter than 8 bytes")

// ErrFrameTooLarge reports a frame larger than the decoder supports (and than ASPEED video
// engines produce); a corrupt header must not trigger a huge allocation.
var ErrFrameTooLarge = errors.New("aspeed: frame larger than 1920x1200")

// Largest frame the decoder accepts; the reference's YUV buffer is sized for it.
const (
	MaxWidth  = 1920
	MaxHeight = 1200
)

// Block header codes (top 4 bits of the current word).
const (
	jpegNoSkip      = 0x00
	jpegNoSkipPass2 = 0x02
	lowJPEGNoSkip   = 0x04
	vqNoSkip1Color  = 0x05
	vqNoSkip2Color  = 0x06
	vqNoSkip4Color  = 0x07
	jpegSkip        = 0x08
	frameEnd        = 0x09
	jpegSkipPass2   = 0x0a
	lowJPEGSkip     = 0x0c
	vqSkip1Color    = 0x0d
	vqSkip2Color    = 0x0e
	vqSkip4Color    = 0x0f

	vqNoUpdateLength  = 0x03
	vqUpdateLength    = 0x1B
	blockStartLength  = 0x04
	blockSkipLength   = 20
	yuvBufferCapacity = 3 * 1920 * 1200
)

// Decoder decodes successive ASPEED frames. Frames update blocks of the previous picture, so one
// Decoder must see every frame of a stream, in order.
type Decoder struct {
	// ReferenceSemantics reproduces the reference exactly, for differential testing. When false
	// (the default), two deliberate deviations keep corrupt input from persisting in a picture:
	// decoding stops once the bitstream's data is exhausted (the reference decodes zero words
	// until its word index passes the byte length), and a resolution change starts from a clean
	// buffer (the reference reinterprets the old picture at the new stride).
	ReferenceSemantics bool

	vqColor      [4]uint32
	vqIndex      [4]uint8
	vqBitMapBits int

	htDC, htAC [2]*huffmanTable
	qt         [4][64]int32

	rangeLimit [5*256 + 128]uint8
	yuv        []byte // YUV of the last 4:4:4 frame, for pass-2 refinement
	block      [768]uint8
	coeff      [384]int16
	workspace  [64]int32

	dcY, dcCb, dcCr int16

	first                       bool
	luma, chroma, advance       byte
	mode420                     byte
	src                         []byte
	index                       int // next 32-bit word to load
	bits                        int
	curData, nextData           uint32
	mbx, mby, mbWidth, mbHeight int
	width, height               int
	out                         []byte
}

// NewDecoder returns a decoder with the reference's initial state.
func NewDecoder() *Decoder {
	d := &Decoder{
		first:   true,
		vqIndex: [4]uint8{0, 1, 2, 3},
		vqColor: [4]uint32{0x008080, 0xFF8080, 0x808080, 0xC08080},
		yuv:     make([]byte, yuvBufferCapacity),
	}
	d.htDC[0] = buildHuffmanTable(dcLuminanceSource)
	d.htAC[0] = buildHuffmanTable(acLuminanceSource)
	d.htDC[1] = buildHuffmanTable(dcChrominanceSource)
	d.htAC[1] = buildHuffmanTable(acChrominanceSource)

	r := d.rangeLimit[:]
	for i := 0; i < 256; i++ {
		r[i+256] = uint8(i)
	}
	for i := 512; i < 896; i++ {
		r[i] = 255
	}
	for i := 0; i < 128; i++ {
		r[i+1280] = uint8(i)
	}
	return d
}

// Frame returns the current picture as RGBA (alpha always 255), width*height*4 bytes. It aliases
// the decoder's buffer, which the next Decode updates in place.
func (d *Decoder) Frame() []byte {
	return d.out[:d.width*d.height*4]
}

// Decode applies one frame's bitstream to the picture.
func (d *Decoder) Decode(h Header, bitstream []byte) error {
	if h.Width < 0 || h.Height < 0 || h.Width > MaxWidth || h.Height > MaxHeight {
		return ErrFrameTooLarge
	}
	if d.first || d.luma != h.LumaSelector || d.chroma != h.ChromaSelector || d.advance != h.AdvanceSelector {
		d.luma, d.chroma, d.advance = h.LumaSelector, h.ChromaSelector, h.AdvanceSelector
		d.qt[0] = quantTable(&lumaQuantTables, d.luma)
		d.qt[1] = quantTable(&chromaQuantTables, d.chroma)
		d.qt[2] = quantTable(&lumaQuantTables, d.advance)
		d.qt[3] = quantTable(&chromaQuantTables, d.advance)
		d.first = false
	}
	d.mode420 = h.Mode420

	d.index = 2
	d.mbx, d.mby = 0, 0
	d.bits = 32
	d.dcY, d.dcCb, d.dcCr = 0, 0, 0
	if len(bitstream) < 8 {
		return ErrShortBitstream // the reference throws a RangeError here
	}
	d.src = bitstream
	d.curData = binary.LittleEndian.Uint32(bitstream[0:4])
	d.nextData = binary.LittleEndian.Uint32(bitstream[4:8])

	if !d.ReferenceSemantics && (h.Width != d.width || h.Height != d.height) {
		d.out = nil
	}
	d.width, d.height = h.Width, h.Height
	div := 8
	if d.mode420 == 1 {
		div = 16
	}
	mask := div - 1
	d.mbWidth = ((d.width + mask) &^ mask) / div
	d.mbHeight = ((d.height + mask) &^ mask) / div

	if size := d.width * d.height * 4; d.out == nil || len(d.out) < size {
		d.out = make([]byte, size)
		for i := range d.out {
			d.out[i] = 0xff
		}
	}
	d.decodeBuffer(len(bitstream))
	return nil
}

// quantTable returns the table for a selector; selectors above 15 match the reference's empty
// table, which yields zeros like selectors 8-15.
func quantTable(tables *[16][64]int32, sel byte) [64]int32 {
	if int(sel) < len(tables) {
		return tables[sel]
	}
	return [64]int32{}
}

// decodeBuffer walks the block headers. Like the reference it compares the word index with the
// byte length, so after the data it keeps decoding zero words until a frame-end code.
func (d *Decoder) decodeBuffer(length int) {
	for {
		// Both lookahead words lie past the data: nothing real is left to decode.
		if !d.ReferenceSemantics && d.index*4 >= length+8 {
			return
		}
		switch d.currentHeader() {
		case jpegNoSkip:
			d.skipBits(blockStartLength)
			d.decompressJPEG(0)
		case jpegSkip:
			d.updateXY()
			d.skipBits(blockSkipLength)
			d.decompressJPEG(0)
		case jpegNoSkipPass2:
			d.skipBits(blockStartLength)
			d.decompressJPEGPass2(2)
		case jpegSkipPass2:
			d.updateXY()
			d.skipBits(blockSkipLength)
			d.decompressJPEGPass2(2)
		case vqNoSkip1Color:
			d.skipBits(blockStartLength)
			d.decompressVQ(0)
		case vqSkip1Color:
			d.updateXY()
			d.skipBits(blockSkipLength)
			d.decompressVQ(0)
		case vqNoSkip2Color:
			d.skipBits(blockStartLength)
			d.decompressVQ(1)
		case vqSkip2Color:
			d.updateXY()
			d.skipBits(blockSkipLength)
			d.decompressVQ(1)
		case vqNoSkip4Color:
			d.skipBits(blockStartLength)
			d.decompressVQ(2)
		case vqSkip4Color:
			d.updateXY()
			d.skipBits(blockSkipLength)
			d.decompressVQ(2)
		case lowJPEGNoSkip:
			d.skipBits(blockStartLength)
			d.decompressJPEG(2)
		case lowJPEGSkip:
			d.updateXY()
			d.skipBits(blockSkipLength)
			d.decompressJPEG(2)
		case frameEnd:
			return
		default:
			d.skipBits(vqNoUpdateLength)
		}
		d.moveBlockIndex()
		if d.index >= length {
			return
		}
	}
}

// JS masks shift counts to their low 5 bits; Go would shift everything out.
func shl(x uint32, n int) uint32 { return x << (uint(n) & 31) }
func shr(x uint32, n int) uint32 { return x >> (uint(n) & 31) }

func (d *Decoder) showBits(bits int) uint32 { return shr(d.curData, 32-bits) }

func (d *Decoder) getBits(bits int) int64 {
	v := int64(d.showBits(bits))
	// bits is a 4-bit size field (at most 15), so negPow2 is always in range; the guard only
	// keeps malformed input from panicking.
	if int32(shl(1, bits-1))&int32(uint32(v)) == 0 && bits >= 0 && bits < len(negPow2) {
		v += int64(negPow2[bits])
	}
	d.skipBits(bits)
	return v
}

func (d *Decoder) skipBits(bits int) {
	newBits := d.bits
	if newBits-bits <= 0 {
		offset := d.index << 2
		var data uint32
		if offset+4 <= len(d.src) {
			data = binary.LittleEndian.Uint32(d.src[offset:])
		}
		d.index++
		d.curData = shl(d.curData, bits) | shr(d.nextData|shr(data, newBits), 32-bits)
		d.nextData = shl(data, bits-newBits)
		d.bits = 32 + newBits - bits
	} else {
		d.curData = shl(d.curData, bits) | shr(d.nextData, 32-bits)
		d.nextData = shl(d.nextData, bits)
		d.bits -= bits
	}
}

func (d *Decoder) currentHeader() uint32 { return (d.curData >> 28) & 0x0f }

func (d *Decoder) updateXY() {
	d.mbx = int((d.curData & 0x0FF00000) >> 20)
	d.mby = int((d.curData & 0x0FF000) >> 12)
}

func (d *Decoder) moveBlockIndex() {
	d.mbx++
	if d.mbx >= d.mbWidth {
		d.mby++
		if d.mby >= d.mbHeight {
			d.mby = 0
		}
		d.mbx = 0
	}
}

func (d *Decoder) decompressJPEG(table int) {
	d.decodeHuffmanDataUnit(0, &d.dcY, 0)
	d.inverseDCT(0, table)
	if d.mode420 == 1 {
		for _, pos := range []int{64, 128, 192} {
			d.decodeHuffmanDataUnit(0, &d.dcY, pos)
			d.inverseDCT(pos, table)
		}
		d.decodeHuffmanDataUnit(1, &d.dcCb, 256)
		d.inverseDCT(256, table+1)
		d.decodeHuffmanDataUnit(1, &d.dcCr, 320)
		d.inverseDCT(320, table+1)
	} else {
		d.decodeHuffmanDataUnit(1, &d.dcCb, 64)
		d.inverseDCT(64, table+1)
		d.decodeHuffmanDataUnit(1, &d.dcCr, 128)
		d.inverseDCT(128, table+1)
	}
	d.convertYUVToRGB()
}

func (d *Decoder) decompressJPEGPass2(table int) {
	d.decodeHuffmanDataUnit(0, &d.dcY, 0)
	d.inverseDCT(0, table)
	d.decodeHuffmanDataUnit(1, &d.dcCb, 64)
	d.inverseDCT(64, table+1)
	d.decodeHuffmanDataUnit(1, &d.dcCr, 128)
	d.inverseDCT(128, table+1)
	d.convertYUVToRGBPass2()
}

// decompressVQ fills one 8x8 block from up to four palette colours. bitMapBits is 0 (one colour),
// 1 (two) or 2 (four); the palette entries are updated from the stream first.
func (d *Decoder) decompressVQ(bitMapBits int) {
	d.vqBitMapBits = bitMapBits
	for i := 0; i < 1<<bitMapBits; i++ {
		d.updateVQColor(i)
	}
	if bitMapBits == 0 {
		c := d.vqColor[d.vqIndex[0]]
		for n := 0; n < 64; n++ {
			d.block[n] = uint8((c & 0xFF0000) >> 16)
			d.block[n+64] = uint8((c & 0xFF00) >> 8)
			d.block[n+128] = uint8(c & 0xFF)
		}
	} else {
		for n := 0; n < 64; n++ {
			c := d.vqColor[d.vqIndex[d.showBits(bitMapBits)]]
			d.block[n] = uint8((c & 0xFF0000) >> 16)
			d.block[n+64] = uint8((c & 0xFF00) >> 8)
			d.block[n+128] = uint8(c & 0xFF)
			d.skipBits(bitMapBits)
		}
	}
	d.convertYUVToRGB()
}

func (d *Decoder) updateVQColor(i int) {
	d.vqIndex[i] = uint8((d.curData >> 29) & 0x03)
	if (d.curData>>31)&0x01 == 0 {
		d.skipBits(vqNoUpdateLength)
	} else {
		d.vqColor[d.vqIndex[i]] = (d.curData >> 5) & 0xFFFFFF
		d.skipBits(vqUpdateLength)
	}
}

// symbolIndex mirrors the reference's WORD_hi_lo(k, tmp - minor_code[k]). A k outside the table
// reads undefined, the subtraction is NaN, and NaN & 0xff is 0.
func symbolIndex(ht *huffmanTable, k int, tmp uint32) int {
	lo := int32(0)
	if k >= 0 && k < len(ht.minorCode) {
		lo = int32(int64(tmp) - int64(ht.minorCode[k]))
	}
	return int((int32(shl(uint32(int32(k)), 8)) | (lo & 0xff)) & 0xffff)
}

func (d *Decoder) decodeHuffmanDataUnit(table int, preDC *int16, pos int) {
	htDC, htAC := d.htDC[table], d.htAC[table]
	for i := pos; i < pos+64; i++ {
		d.coeff[i] = 0
	}

	k := int(htDC.len[(d.curData>>16)&0xffff])
	tmp := d.showBits(k)
	d.skipBits(k)
	if size := int(htDC.v[symbolIndex(htDC, k, tmp)]); size == 0 {
		d.coeff[pos] = *preDC
	} else {
		v := int16(int64(*preDC) + d.getBits(size))
		*preDC = v
		d.coeff[pos] = v
	}

	for nr := 1; nr < 64; {
		k = int(htAC.len[(d.curData>>16)&0xffff])
		tmp = d.showBits(k)
		d.skipBits(k)
		sym := int32(htAC.v[symbolIndex(htAC, k, tmp)])
		size := int(sym & 0x0f)
		count := int((sym >> 4) & 0x0f)
		if size == 0 {
			if count != 0x0f {
				break
			}
			nr += 16
			continue
		}
		nr += count
		d.coeff[pos+int(dezigzag[nr])] = int16(d.getBits(size))
		nr++
	}
}

// inverseDCT is the reference's AAN integer IDCT. All arithmetic wraps at 32 bits, matching the
// reference's int32 shifts and Uint32Array workspace.
func (d *Decoder) inverseDCT(index, table int) {
	const (
		fix1082392200 = 277
		fix1414213562 = 362
		fix1847759065 = 473
		fix2613125930 = 669
	)
	qt := &d.qt[table]
	c := &d.coeff
	ws := &d.workspace
	deq := func(i, j int) int32 { return int32(int64(qt[i])*int64(c[j])) >> 16 }

	in := index
	for col := 0; col < 8; col, in = col+1, in+1 {
		if c[in+8] == 0 && c[in+16] == 0 && c[in+24] == 0 && c[in+32] == 0 &&
			c[in+40] == 0 && c[in+48] == 0 && c[in+56] == 0 {
			dc := deq(col, in)
			for row := 0; row < 64; row += 8 {
				ws[col+row] = dc
			}
			continue
		}

		tmp0, tmp4 := deq(col, in), deq(col+8, in+8)
		tmp1, tmp5 := deq(col+16, in+16), deq(col+24, in+24)
		tmp2, tmp6 := deq(col+32, in+32), deq(col+40, in+40)
		tmp3, tmp7 := deq(col+48, in+48), deq(col+56, in+56)

		tmp10 := tmp0 + tmp2
		tmp11 := tmp0 - tmp2
		tmp13 := tmp1 + tmp3
		tmp12 := (((tmp1 - tmp3) * fix1414213562) >> 8) - tmp13
		tmp0 = tmp10 + tmp13
		tmp3 = tmp10 - tmp13
		tmp1 = tmp11 + tmp12
		tmp2 = tmp11 - tmp12

		z13 := tmp6 + tmp5
		z10 := tmp6 - tmp5
		z11 := tmp4 + tmp7
		z12 := tmp4 - tmp7
		tmp7 = z11 + z13
		tmp11 = ((z11 - z13) * fix1414213562) >> 8
		z5 := ((z10 + z12) * fix1847759065) >> 8
		tmp10 = ((z12 * fix1082392200) >> 8) - z5
		tmp12 = ((z10 * -fix2613125930) >> 8) + z5
		tmp6 = tmp12 - tmp7
		tmp5 = tmp11 - tmp6
		tmp4 = tmp10 + tmp5

		ws[col] = tmp0 + tmp7
		ws[col+8] = tmp1 + tmp6
		ws[col+16] = tmp2 + tmp5
		ws[col+24] = tmp3 - tmp4
		ws[col+32] = tmp3 + tmp4
		ws[col+40] = tmp2 - tmp5
		ws[col+48] = tmp1 - tmp6
		ws[col+56] = tmp0 - tmp7
	}

	limit := d.rangeLimit[384:]
	for row := 0; row < 64; row += 8 {
		tmp10 := ws[row] + ws[row+4]
		tmp11 := ws[row] - ws[row+4]
		tmp13 := ws[row+2] + ws[row+6]
		tmp12 := (((ws[row+2] - ws[row+6]) * fix1414213562) >> 8) - tmp13
		tmp0 := tmp10 + tmp13
		tmp3 := tmp10 - tmp13
		tmp1 := tmp11 + tmp12
		tmp2 := tmp11 - tmp12

		z13 := ws[row+5] + ws[row+3]
		z10 := ws[row+5] - ws[row+3]
		z11 := ws[row+1] + ws[row+7]
		z12 := ws[row+1] - ws[row+7]
		tmp7 := z11 + z13
		tmp11 = ((z11 - z13) * fix1414213562) >> 8
		z5 := ((z10 + z12) * fix1847759065) >> 8
		tmp10 = ((z12 * fix1082392200) >> 8) - z5
		tmp12 = ((z10 * -fix2613125930) >> 8) + z5
		tmp6 := tmp12 - tmp7
		tmp5 := tmp11 - tmp6
		tmp4 := tmp10 + tmp5

		out := d.block[index+row : index+row+8]
		out[0] = limit[((tmp0+tmp7)>>3)&1023]
		out[1] = limit[((tmp1+tmp6)>>3)&1023]
		out[2] = limit[((tmp2+tmp5)>>3)&1023]
		out[3] = limit[((tmp3-tmp4)>>3)&1023]
		out[4] = limit[((tmp3+tmp4)>>3)&1023]
		out[5] = limit[((tmp2-tmp5)>>3)&1023]
		out[6] = limit[((tmp1-tmp6)>>3)&1023]
		out[7] = limit[((tmp0-tmp7)>>3)&1023]
	}
}

// clampByte mirrors Math.floor(v).clamp(0, 255) stored into a Uint8Array: NaN becomes 0.
func clampByte(v float64) byte {
	v = math.Floor(v)
	switch {
	case math.IsNaN(v), v <= 0:
		return 0
	case v >= 255:
		return 255
	}
	return byte(v)
}

// clampJS mirrors Number.clamp(0, 255) without flooring; NaN stays NaN.
func clampJS(v float64) float64 {
	return math.Min(math.Max(v, 0), 255)
}

// putRGB writes one pixel, dropping out-of-range bytes as a JS typed array does.
func (d *Decoder) putRGB(n int, y, cb, cr float64) {
	px := [4]byte{
		clampByte(y + 1.402*(cr-128)),
		clampByte(y - 0.3441363*(cb-128) - (0.71413636 * (cr - 128))),
		clampByte(y + 1.772*(cb-128)),
		0xff,
	}
	for i, v := range px {
		if n+i >= 0 && n+i < len(d.out) {
			d.out[n+i] = v
		}
	}
}

func (d *Decoder) putYUV(idx int, v byte) {
	if idx >= 0 && idx < len(d.yuv) {
		d.yuv[idx] = v
	}
}

// getYUV reads the saved YUV, as NaN where the reference would read undefined.
func (d *Decoder) getYUV(idx int) float64 {
	if idx < 0 || idx >= len(d.yuv) {
		return math.NaN()
	}
	return float64(d.yuv[idx])
}

func (d *Decoder) convertYUVToRGB() {
	w := d.width
	if d.mode420 == 0 {
		limit := w * d.height * 4
		pixelX, pixelY := d.mbx*8, d.mby*8
		pos := pixelY*w + pixelX
		cols := w - pixelX
		if cols == 0 || cols > 8 {
			cols = 8
		}
		for j := 0; j < 8; j++ {
			n, m := pos<<2, j<<3
			for i := 0; i < cols; i, n, m = i+1, n+4, m+1 {
				if n >= limit {
					continue
				}
				y, cb, cr := d.block[m], d.block[m+64], d.block[m+128]
				d.putYUV((pos+i)*3, y)
				d.putYUV((pos+i)*3+1, cb)
				d.putYUV((pos+i)*3+2, cr)
				d.putRGB(n, float64(y), float64(cb), float64(cr))
			}
			pos += w
		}
		return
	}

	// 4:2:0: four 8x8 luma blocks and one 8x8 block each of Cb and Cr cover 16x16 pixels. Rows
	// are written at full width without clipping, as in the reference.
	pos := d.mby*16*w + d.mbx*16
	rows := 16
	if d.height == 608 && d.mby == 37 { // 800x600 support in the reference
		rows = 8
	}
	idx := 0
	for j := 0; j < rows; j++ {
		n := pos * 4
		for i := 0; i < 16; i++ {
			m := int(yuv420Chroma[idx])
			y := d.block[int(yuv420Block[idx])*64+int(yuv420Index[idx])]
			d.putRGB(n, float64(y), float64(d.block[256+m]), float64(d.block[320+m]))
			idx++
			n += 4
		}
		pos += w
	}
}

// convertYUVToRGBPass2 refines a 4:4:4 block with a second-pass delta. The reference ignores
// pass-2 blocks in 4:2:0 mode.
func (d *Decoder) convertYUVToRGBPass2() {
	if d.mode420 != 0 {
		return
	}
	w := d.width
	limit := w * d.height * 4
	pixelX, pixelY := d.mbx*8, d.mby*8
	pos := pixelY*w + pixelX
	cols := w - pixelX
	if cols == 0 || cols > 8 {
		cols = 8
	}
	for j := 0; j < 8; j++ {
		n, m := pos<<2, j<<3
		for i := 0; i < cols; i, n, m = i+1, n+4, m+1 {
			if n >= limit {
				continue
			}
			y := clampJS(d.getYUV((pos+i)*3) + (float64(d.block[m]) - 128))
			cb := clampJS(d.getYUV((pos+i)*3+1) + (float64(d.block[m+64]) - 128))
			cr := clampJS(d.getYUV((pos+i)*3+2) + (float64(d.block[m+128]) - 128))
			d.putRGB(n, y, cb, cr)
		}
		pos += w
	}
}
