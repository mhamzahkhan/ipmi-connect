package web

import (
	"encoding/json"
	"log"
	"slices"

	"ipmi-connect/internal/bmc"
)

// inputState forwards one browser's input to the console and remembers what that browser is
// holding, so lost focus or a closed tab can release it instead of leaving keys down on the host.
type inputState struct {
	console bmc.Console
	held    map[uint16]bool
	buttons byte
	x, y    uint16
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
}

// handle dispatches one browser message. Malformed messages are ignored.
func (in *inputState) handle(data []byte) {
	var msg clientMessage
	if err := json.Unmarshal(data, &msg); err != nil {
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
	if err := in.console.SendKeyEvent(down, usage); err != nil {
		log.Printf("[Input] Key %d: %v", usage, err)
	}
}

func (in *inputState) pointer(buttons byte, x, y uint16) {
	in.buttons, in.x, in.y = buttons, x, y
	if err := in.console.SendPointerEvent(buttons, x, y); err != nil {
		log.Printf("[Input] Pointer: %v", err)
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
