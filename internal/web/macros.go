package web

import (
	"log"
	"time"

	"ipmi-connect/internal/bmc"
)

// keyMacro is a chord: keys are pressed in order, held, then released in reverse order.
type keyMacro struct {
	keys []uint32
	hold time.Duration
}

const macroTapHold = 50 * time.Millisecond

// macros are chords of USB HID usages, which every supported console accepts.
var macros = map[string]keyMacro{
	"cad":          {keys: []uint32{224, 226, 76}, hold: 60 * time.Millisecond}, // LCtrl+LAlt+Delete
	"f1":           {keys: []uint32{58}, hold: macroTapHold},
	"f2":           {keys: []uint32{59}, hold: macroTapHold},
	"f10":          {keys: []uint32{67}, hold: macroTapHold},
	"f11":          {keys: []uint32{68}, hold: macroTapHold},
	"f12":          {keys: []uint32{69}, hold: macroTapHold},
	"esc":          {keys: []uint32{41}, hold: macroTapHold},
	"win":          {keys: []uint32{227}, hold: macroTapHold},     // LMeta
	"alt_tab":      {keys: []uint32{226, 43}, hold: macroTapHold}, // LAlt+Tab
	"alt_f4":       {keys: []uint32{226, 61}, hold: macroTapHold}, // LAlt+F4
	"print_screen": {keys: []uint32{70}, hold: macroTapHold},
}

// playMacro sends every press, holds, then sends every release, even if some sends fail,
// so a partial failure never leaves a modifier stuck down. It returns the first error.
func playMacro(m keyMacro, send func(down bool, key uint32) error, sleep func(time.Duration)) error {
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	for _, k := range m.keys {
		record(send(true, k))
	}
	sleep(m.hold)
	for i := len(m.keys) - 1; i >= 0; i-- {
		record(send(false, m.keys[i]))
	}
	return firstErr
}

// executeMacro plays a named macro on the console.
func executeMacro(console bmc.Console, name string) {
	m, ok := macros[name]
	if !ok {
		log.Printf("[Server] Unknown macro %q, ignoring", name)
		return
	}
	log.Printf("[Server] Executing macro key sequence: %s", name)
	send := func(down bool, key uint32) error { return console.SendKeyEvent(down, uint16(key)) }
	if err := playMacro(m, send, time.Sleep); err != nil {
		log.Printf("[Server] Macro %q: %v", name, err)
	}
}
