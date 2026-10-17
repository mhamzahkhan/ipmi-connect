// Ported from Aspeed's huffmantable.js (Copyright (C) 2021 Aspeed Tech., MPL 2.0).

package aspeed

// huffmanSource holds the JPEG-standard tables a lookup table is built from.
type huffmanSource struct {
	nrCodes []uint8  // number of codes of each bit length (index 1..16)
	values  []uint8  // symbol values in code order
	code    []uint16 // code boundaries, consumed in pairs
	codeLow []uint16 // code lengths, read at odd indexes
}

// huffmanTable mirrors the JS typed arrays (Int8Array/Int16Array), including their wrapping.
type huffmanTable struct {
	length    [17]int8
	minorCode [17]int16
	majorCode [17]int16
	v         [65536]int16 // symbol by (code length << 8 | code offset)
	len       [65536]int8  // code length by the next 16 bits of input
}

// at returns s[i], or 0 where JS would read undefined (stored as 0 in a typed array).
func at[T uint8 | uint16](s []T, i int) T {
	if i < 0 || i >= len(s) {
		return 0
	}
	return s[i]
}

func buildHuffmanTable(src huffmanSource) *huffmanTable {
	ht := &huffmanTable{}
	i, code := 0, 0
	for k := 1; k <= 16; k++ {
		ht.length[k] = int8(at(src.nrCodes, k))
		n := int(uint8(ht.length[k]))
		for j := 0; j < n; j++ {
			ht.v[((k&0xFF)<<8)|(j&0xFF)] = int16(at(src.values, i))
			i++
		}
		ht.minorCode[k] = int16(code)
		code += n
		ht.majorCode[k] = int16(code - 1)
		code *= 2
		if n == 0 {
			ht.minorCode[k] = -1 // 0xffff in an Int16Array
			ht.majorCode[k] = 0
		}
	}

	ht.len[0] = 2
	i = 2
	for ci := 1; ci < 65536; ci++ {
		// Past the end of code, JS compares against undefined, which is never true.
		if i < len(src.code) && ci >= int(src.code[i]) {
			i += 2
		}
		ht.len[ci] = int8(at(src.codeLow, i+1))
	}
	return ht
}
