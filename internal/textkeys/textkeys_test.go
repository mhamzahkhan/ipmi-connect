package textkeys

import (
	"reflect"
	"testing"
)

func TestEncode(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    []Key
		skipped string
	}{
		{"lower and upper case", "aZ", []Key{{0x04, false}, {0x1D, true}}, ""},
		{"digits and shifted symbols", "1!0)", []Key{{0x1E, false}, {0x1E, true}, {0x27, false}, {0x27, true}}, ""},
		{"punctuation", "-_=+[{]}\\|;:'\",<.>/?`~", []Key{
			{0x2D, false}, {0x2D, true}, {0x2E, false}, {0x2E, true}, {0x2F, false}, {0x2F, true},
			{0x30, false}, {0x30, true}, {0x31, false}, {0x31, true}, {0x33, false}, {0x33, true},
			{0x34, false}, {0x34, true}, {0x36, false}, {0x36, true}, {0x37, false}, {0x37, true},
			{0x38, false}, {0x38, true}, {0x35, false}, {0x35, true},
		}, ""},
		{"whitespace", " \t\n", []Key{{0x2C, false}, {0x2B, false}, {0x28, false}}, ""},
		{"CRLF is one Enter", "a\r\nb", []Key{{0x04, false}, {0x28, false}, {0x05, false}}, ""},
		{"lone CR is Enter", "a\rb", []Key{{0x04, false}, {0x28, false}, {0x05, false}}, ""},
		{"untypeable characters are skipped and reported", "café 💥", []Key{{0x06, false}, {0x04, false}, {0x09, false}, {0x2C, false}}, "é💥"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, skipped := Encode(tt.text)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("keys %v, want %v", got, tt.want)
			}
			if skipped != tt.skipped {
				t.Errorf("skipped %q, want %q", skipped, tt.skipped)
			}
		})
	}
}
