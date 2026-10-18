package web

import (
	"reflect"
	"sync"
	"testing"

	"github.com/mhamzahkhan/ipmi-connect/internal/bmc"
)

type pointerCall struct {
	buttons byte
	x, y    uint16
}

// fakeConsole records what the web layer sends to the BMC.
type fakeConsole struct {
	mu        sync.Mutex
	keys      []keyEvent
	pointers  []pointerCall
	refreshes int
	power     []bmc.PowerAction
}

func (f *fakeConsole) SendPointerEvent(buttons byte, x, y uint16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pointers = append(f.pointers, pointerCall{buttons, x, y})
	return nil
}

func (f *fakeConsole) SendKeyEvent(down bool, usage uint16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, keyEvent{down: down, key: uint32(usage)})
	return nil
}

func (f *fakeConsole) RequestFrameRefresh() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes++
	return nil
}

func (f *fakeConsole) snapshot() (keys []keyEvent, refreshes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]keyEvent(nil), f.keys...), f.refreshes
}

func (f *fakeConsole) PowerControl(a bmc.PowerAction) error {
	f.power = append(f.power, a)
	return nil
}

func TestInputStateReleasesEverythingHeld(t *testing.T) {
	fc := &fakeConsole{}
	in := newInputState(fc)

	in.key(true, 224)      // LCtrl down
	in.key(true, 4)        // A down
	in.key(false, 4)       // A up
	in.key(true, 226)      // LAlt down
	in.pointer(5, 100, 50) // left+right held at 100,50
	fc.keys, fc.pointers = nil, nil

	in.releaseAll()

	wantKeys := []keyEvent{{down: false, key: 224}, {down: false, key: 226}}
	if !reflect.DeepEqual(fc.keys, wantKeys) {
		t.Errorf("released keys %v, want %v", fc.keys, wantKeys)
	}
	wantPtr := []pointerCall{{0, 100, 50}}
	if !reflect.DeepEqual(fc.pointers, wantPtr) {
		t.Errorf("pointer %v, want %v", fc.pointers, wantPtr)
	}

	fc.keys, fc.pointers = nil, nil
	in.releaseAll()
	if len(fc.keys) != 0 || len(fc.pointers) != 0 {
		t.Errorf("second release sent %v %v; nothing is held", fc.keys, fc.pointers)
	}
}

func TestInputStateIgnoresInvalidKeys(t *testing.T) {
	fc := &fakeConsole{}
	in := newInputState(fc)
	in.key(true, 0)
	if len(fc.keys) != 0 {
		t.Errorf("usage 0 (unmapped key) was sent: %v", fc.keys)
	}
}

func TestHandleClientInputRoutesToConsole(t *testing.T) {
	fc := &fakeConsole{}
	in := newInputState(fc)

	in.handle([]byte(`{"type":"key","down":true,"hidCode":4}`))
	in.handle([]byte(`{"type":"pointer","mask":1,"x":10,"y":20}`))
	in.handle([]byte(`{"type":"refresh"}`))
	in.handle([]byte(`{"type":"release"}`))
	in.handle([]byte(`not json`))

	if want := []keyEvent{{true, 4}, {false, 4}}; !reflect.DeepEqual(fc.keys, want) {
		t.Errorf("keys %v, want %v", fc.keys, want)
	}
	if want := []pointerCall{{1, 10, 20}, {0, 10, 20}}; !reflect.DeepEqual(fc.pointers, want) {
		t.Errorf("pointers %v, want %v", fc.pointers, want)
	}
	if fc.refreshes != 1 {
		t.Errorf("refreshes = %d", fc.refreshes)
	}
}

func TestLiveInputIsIgnoredWhileTyping(t *testing.T) {
	fc := &fakeConsole{}
	in := newInputState(fc)
	in.typing.Store(true)
	in.handle([]byte(`{"type":"key","down":true,"hidCode":4}`))
	in.handle([]byte(`{"type":"pointer","mask":1,"x":5,"y":5}`))
	if len(fc.keys) != 0 || len(fc.pointers) != 0 {
		t.Errorf("live input reached the host during a paste: keys %v pointers %v", fc.keys, fc.pointers)
	}
}
