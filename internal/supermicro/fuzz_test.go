package supermicro

import (
	"bufio"
	"bytes"
	"io"
	"log"
	"os"
	"testing"
)

// checkSink checks what a session hands to the browser.
type checkSink struct{ t *testing.T }

func (s checkSink) UpdateScreen(w, h int, pix []byte) {
	if w <= 0 || h <= 0 || w > maxScreenSide || h > maxScreenSide || len(pix) != w*h*4 {
		s.t.Fatalf("UpdateScreen(%d, %d, %d bytes)", w, h, len(pix))
	}
}
func (checkSink) SetStatus(string) {}
func (checkSink) SetFPS(int)       {}

// FuzzReadMessage feeds BMC bytes through the message reader and the session's handling of what
// it returns (resizes and Hermon decoding included).
func FuzzReadMessage(f *testing.F) {
	f.Add(fbu(rect(0, 0, 32, 16, 0x59, hermon(0, 0, 1, tile(0, 1, []byte{0x00, 0x7C}, 256)))))
	f.Add(fbu(rect(0, 0, 2, 1, 0x59, hermon(1, 0, 0, []byte{0x1F, 0x00, 0xE0, 0x03}))))
	f.Add(fbu(rect(0, 0, 16, 16, 0x00, hermon(0, 1, 1, tile(0, 0, []byte{0x31}, 256))), rect(0, 0, screenOffW, screenOffH, 0x59, nil)))
	f.Add([]byte{0x16, 0x00, 0x37, 1, 2, 1, 0x35, 0, 0, 0, 0, 0, 0x3c, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Add(append([]byte{0x39, 0, 0, 0, 1, 0, 0, 0, 4}, make([]byte, 256)...))
	f.Add(append([]byte{0x04, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 2, 0, 0, 0, 1}, make([]byte, 12)...))
	f.Add([]byte{0x00, 0x00, 0xff, 0xff})
	log.SetOutput(io.Discard) // resizes and notices log
	f.Cleanup(func() { log.SetOutput(os.Stderr) })
	f.Fuzz(func(t *testing.T, data []byte) {
		r := bufio.NewReader(bytes.NewReader(data))
		s := newSession(nil, r, ServerInfo{Video: true, KeyboardMouse: true})
		for {
			m, err := readMessage(r, mouseInfoLen)
			if err != nil {
				return
			}
			if u, ok := m.(FramebufferUpdate); ok {
				total := 0
				for _, rc := range u.Rects {
					total += len(rc.Data)
				}
				if total > maxUpdateData {
					t.Fatalf("update carries %d bytes", total)
				}
			}
			s.handle(m, checkSink{t})
			for len(s.out) > 0 { // nothing writes in this test: keep the queue from filling
				<-s.out
			}
		}
	})
}

// FuzzHandshake plays arbitrary bytes as the BMC's side of the handshake.
func FuzzHandshake(f *testing.F) {
	ok := func(sec []byte, result byte, after []byte) []byte {
		b := append([]byte("RFB 003.008\n"), byte(len(sec)))
		b = append(b, sec...)
		b = append(b, bytes.Repeat([]byte{0xa7}, 24)...)
		b = append(b, 0, 0, 0, result)
		return append(b, after...)
	}
	f.Add(ok([]byte{0x10}, 0, serverInit("ATEN iKVM Server", 0x1234, 1, 1, 1, 1)))
	f.Add(ok([]byte{0x01, 0x02}, 0, serverInit("x", 1, 0, 0, 0, 0)))
	f.Add(ok([]byte{0x10}, 1, []byte{0, 0, 0, 7, 'r', 'e', 'f', 'u', 's', 'e', 'd'}))
	f.Add([]byte("RFB 003.008\n\x00\x00\x00\x00\x04nope"))
	f.Fuzz(func(t *testing.T, data []byte) {
		info, err := handshake(bufio.NewReader(bytes.NewReader(data)), io.Discard, Ticket{User: "tok", Password: "tok"})
		if err == nil && len(info.Name) > maxWireString {
			t.Fatalf("name of %d bytes", len(info.Name))
		}
	})
}

// FuzzParseTicket parses arbitrary JNLP bodies.
func FuzzParseTicket(f *testing.F) {
	if data, err := os.ReadFile("testdata/ikvm.jnlp"); err == nil {
		f.Add(string(data), "192.0.2.20")
	}
	f.Add("<jnlp><argument>192.0.2.20</argument><argument>t</argument><argument>t</argument><argument>null</argument>"+
		"<argument>63630</argument><argument>623</argument><argument>2</argument><argument>0</argument>"+
		"<argument>1</argument><argument>5900</argument></jnlp>", "192.0.2.20")
	f.Add("<jnlp><argument>a&amp;b</argument><argument>&#x41;</argument><argument></argument></jnlp>", "")
	f.Fuzz(func(t *testing.T, body, host string) {
		tk, err := parseTicket(body, host)
		if err != nil {
			return
		}
		if tk.Port <= 0 || tk.Port >= 65536 || len(tk.User) > maxTicketToken || len(tk.Password) > maxTicketToken {
			t.Fatalf("ticket %+v", tk)
		}
		if n := len(credentials(tk.User, tk.Password)); n != 48 {
			t.Fatalf("credentials are %d bytes", n)
		}
	})
}

// FuzzDecodeHermon decodes arbitrary payloads into pictures of arbitrary (plausible) size.
func FuzzDecodeHermon(f *testing.F) {
	f.Add(uint16(64), uint16(32), hermon(0, 0, 1, tile(1, 2, []byte{0x00, 0x7C}, 256)))
	f.Add(uint16(10), uint16(6), hermon(0, 0, 1, tile(0, 0, []byte{0xFF, 0x7F}, 256)))
	f.Add(uint16(2), uint16(1), hermon(1, 0, 0, []byte{0x1F, 0x00, 0xE0, 0x03}))
	f.Add(uint16(16), uint16(16), hermon(0, 1, 1, tile(0, 0, []byte{0x31}, 256)))
	f.Add(uint16(32), uint16(16), hermon(0, 0, 1000, tile(0, 1, []byte{0x00, 0x7C}, 256)))
	f.Add(uint16(1), uint16(1), hermon(1, 1, 0, []byte{0xff, 0xff, 0xff}))
	f.Fuzz(func(t *testing.T, w, h uint16, data []byte) {
		if w == 0 || h == 0 || int(w)*int(h) > 1<<20 { // the session skips empty rects; keep runs fast
			t.Skip()
		}
		fr := newFrame(int(w), int(h))
		_ = decodeHermon(data, fr)
		if len(fr.pix) != int(w)*int(h)*4 {
			t.Fatalf("picture resized to %d bytes", len(fr.pix))
		}
		for i := 3; i < len(fr.pix); i += 4 {
			if fr.pix[i] != 0xff {
				t.Fatalf("alpha at byte %d is %d", i, fr.pix[i])
			}
		}
	})
}
