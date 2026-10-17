package dell

import "testing"

func TestEncodeSetValue(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"plainpassword", "plainpassword"},
		{"pass:word", "pass\\:word"},
		{"p=a&s,s\\", "p\\=a\\&s\\,s\\\\"},
		{"(test)?", "\\(test\\)\\?"},
	}

	for _, tt := range tests {
		got := encodeSetValue(tt.input)
		if got != tt.expected {
			t.Errorf("encodeSetValue(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
