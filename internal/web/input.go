package web

import (
	"encoding/json"
	"log"
	"slices"
	"sync/atomic"

	"ipmi-connect/internal/bmc"
)

// inputState forwards one browser's input to the console and remembers what that browser is
// holding, so lost focus or a closed tab can release it instead of leaving keys down on the host.
type inputState struct {
	console bmc.Console
	held    map[uint16]bool
	buttons byte
	x, y    uint16
	lastErr string // last logged input error, so a persistent failure logs once, not per event
	// typing is set while a paste is being typed: live keys and pointer moves from this browser
	// are dropped so they cannot interleave with it.
	typing atomic.Bool
}

func newInputState(console bmc.Console) *inputState {
	return &inputState{console: console, held: map[uint16]bool{}}
}

// clientMessage is a JSON message from the browser.
type clientMessage struct {
	Type    string `json:"type"`
	Down    bool   `json:"down"`
	HIDCode uint16 `json:"hidCode"`
	Mask    byte   `json:"mask"`
	X       uint16 `json:"x"`
	Y       uint16 `json:"y"`
	Name    string `json:"name"`
	Text    string `json:"text"`
}

// handle dispatches one browser message. Malformed messages are ignored.
func (in *inputState) handle(data []byte) {
	var msg clientMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}
	if in.typing.Load() && (msg.Type == "key" || msg.Type == "pointer" || msg.Type == "macro") {
		return
	}
	switch msg.Type {
	case "key":
		in.key(msg.Down, msg.HIDCode)
	case "pointer":
		in.pointer(msg.Mask, msg.X, msg.Y)
	case "release":
		in.releaseAll()
	case "macro":
		executeMacro(in.console, msg.Name)
	case "refresh":
		if err := in.console.RequestFrameRefresh(); err != nil {
			log.Printf("[Input] Refresh: %v", err)
		}
	}
}

func (in *inputState) key(down bool, usage uint16) {
	if usage == 0 {
		return // key with no HID mapping
	}
	if down {
		in.held[usage] = true
	} else {
		delete(in.held, usage)
	}
	in.report(in.console.SendKeyEvent(down, usage))
}

func (in *inputState) pointer(buttons byte, x, y uint16) {
	in.buttons, in.x, in.y = buttons, x, y
	in.report(in.console.SendPointerEvent(buttons, x, y))
}

// report logs an input error once until input succeeds again: pointer moves arrive dozens of
// times a second and would otherwise bury the log.
func (in *inputState) report(err error) {
	if err == nil {
		in.lastErr = ""
		return
	}
	if msg := err.Error(); msg != in.lastErr {
		in.lastErr = msg
		log.Printf("[Input] %v", err)
	}
}

// releaseAll releases every held key (in a stable order) and mouse button.
func (in *inputState) releaseAll() {
	keys := make([]uint16, 0, len(in.held))
	for k := range in.held {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		in.key(false, k)
	}
	if in.buttons != 0 {
		in.pointer(0, in.x, in.y)
	}
}
