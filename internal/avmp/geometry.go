package avmp

// Standard floppy geometries by block count (512-byte sectors).
var floppyGeometry = map[uint32][3]int{
	720:  {40, 2, 9},  // 360 KB
	1440: {80, 2, 9},  // 720 KB
	2400: {80, 2, 15}, // 1.2 MB
	2880: {80, 2, 18}, // 1.44 MB
	3360: {80, 2, 21}, // 1.68 MB DMF
	5760: {80, 2, 36}, // 2.88 MB
}

const (
	// chsLimit is the largest disk CHS can describe (16383 x 16 x 63); beyond it the vendor
	// sends exactly that, as ATA does.
	chsLimit = 16383 * 16 * 63
	// smallDiskLimit is where 16-head translation runs out of 1024 cylinders.
	smallDiskLimit = 1024 * 16 * 63
)

// geometry returns a cylinder/head/sector geometry for an image of the given block count. The
// vendor derives it from the image's boot sector with some byte-order bugs; standard values are
// what those produce for well-formed images (e.g. 1 GiB gives 130/255/63).
func geometry(blocks uint32) (cyl uint16, heads, sectors byte) {
	if g, ok := floppyGeometry[blocks]; ok {
		return uint16(g[0]), byte(g[1]), byte(g[2])
	}
	if blocks > chsLimit {
		return 16383, 16, 63
	}
	heads, sectors = 16, 63
	if blocks > smallDiskLimit {
		heads = 255
	}
	c := blocks / (uint32(heads) * uint32(sectors))
	return uint16(max(c, 1)), heads, sectors
}
