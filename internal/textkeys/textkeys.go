// Package textkeys turns text into USB HID key presses for a US keyboard layout, for typing a
// paste into a host console.
package textkeys

import "strings"

// Key is one key press: a HID usage on the keyboard page, with or without Shift.
type Key struct {
	Usage uint16
	Shift bool
}

// LeftShift is the HID usage of the left Shift key.
const LeftShift = 0xE1

// unshifted and shifted symbol keys on a US layout, by HID usage.
var symbols = map[rune]Key{
	' ': {0x2C, false}, '\t': {0x2B, false}, '\n': {0x28, false},
	'1': {0x1E, false}, '!': {0x1E, true}, '2': {0x1F, false}, '@': {0x1F, true},
	'3': {0x20, false}, '#': {0x20, true}, '4': {0x21, false}, '$': {0x21, true},
	'5': {0x22, false}, '%': {0x22, true}, '6': {0x23, false}, '^': {0x23, true},
	'7': {0x24, false}, '&': {0x24, true}, '8': {0x25, false}, '*': {0x25, true},
	'9': {0x26, false}, '(': {0x26, true}, '0': {0x27, false}, ')': {0x27, true},
	'-': {0x2D, false}, '_': {0x2D, true}, '=': {0x2E, false}, '+': {0x2E, true},
	'[': {0x2F, false}, '{': {0x2F, true}, ']': {0x30, false}, '}': {0x30, true},
	'\\': {0x31, false}, '|': {0x31, true}, ';': {0x33, false}, ':': {0x33, true},
	'\'': {0x34, false}, '"': {0x34, true}, '`': {0x35, false}, '~': {0x35, true},
	',': {0x36, false}, '<': {0x36, true}, '.': {0x37, false}, '>': {0x37, true},
	'/': {0x38, false}, '?': {0x38, true},
}

// Encode maps text to key presses. CRLF and lone CR become one Enter. Characters a US keyboard
// cannot type are left out and returned in skipped.
func Encode(text string) (keys []Key, skipped string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var skip strings.Builder
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z':
			keys = append(keys, Key{0x04 + uint16(r-'a'), false})
		case r >= 'A' && r <= 'Z':
			keys = append(keys, Key{0x04 + uint16(r-'A'), true})
		default:
			if k, ok := symbols[r]; ok {
				keys = append(keys, k)
			} else {
				skip.WriteRune(r)
			}
		}
	}
	return keys, skip.String()
}
