package web

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

type keyEvent struct {
	down bool
	key  uint32
}

func press(keys ...uint32) []keyEvent {
	ev := make([]keyEvent, 0, len(keys))
	for _, k := range keys {
		ev = append(ev, keyEvent{down: true, key: k})
	}
	return ev
}

func release(keys ...uint32) []keyEvent {
	ev := make([]keyEvent, 0, len(keys))
	for _, k := range keys {
		ev = append(ev, keyEvent{down: false, key: k})
	}
	return ev
}

func seq(parts ...[]keyEvent) []keyEvent {
	var out []keyEvent
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// Expected sequences are what executeMacro sent to the Dell before macros became table-driven.
func TestMacroTablesReplayOriginalSequences(t *testing.T) {
	const tap = 50 * time.Millisecond
	tests := []struct {
		table    string
		macro    string
		want     []keyEvent
		wantHold time.Duration
	}{
		{"dell", "cad", seq(press(224, 226, 76), release(76, 226, 224)), 60 * time.Millisecond},
		{"dell", "f1", seq(press(58), release(58)), tap},
		{"dell", "f2", seq(press(59), release(59)), tap},
		{"dell", "f10", seq(press(67), release(67)), tap},
		{"dell", "f11", seq(press(68), release(68)), tap},
		{"dell", "f12", seq(press(69), release(69)), tap},
		{"dell", "esc", seq(press(41), release(41)), tap},
		{"dell", "win", seq(press(227), release(227)), tap},
		{"dell", "alt_tab", seq(press(226, 43), release(43, 226)), tap},
		{"dell", "alt_f4", seq(press(226, 61), release(61, 226)), tap},
		{"dell", "print_screen", seq(press(70), release(70)), tap},
	}

	tables := map[string]map[string]keyMacro{"dell": macros}
	for _, tt := range tests {
		t.Run(tt.table+"/"+tt.macro, func(t *testing.T) {
			m, ok := tables[tt.table][tt.macro]
			if !ok {
				t.Fatalf("macro %q missing from %s table", tt.macro, tt.table)
			}

			var got []keyEvent
			var slept []time.Duration
			send := func(down bool, key uint32) error {
				got = append(got, keyEvent{down, key})
				return nil
			}
			if err := playMacro(m, send, func(d time.Duration) { slept = append(slept, d) }); err != nil {
				t.Fatalf("playMacro: %v", err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("events\n got  %v\n want %v", got, tt.want)
			}
			if !reflect.DeepEqual(slept, []time.Duration{tt.wantHold}) {
				t.Errorf("sleeps = %v, want [%v]", slept, tt.wantHold)
			}
		})
	}

	if got, want := len(macros), len(tests); got != want {
		t.Errorf("tables define %d macros but %d are pinned by this test", got, want)
	}
}

func TestPlayMacroReleasesKeysEvenWhenAPressFails(t *testing.T) {
	failing := errors.New("socket closed")
	var released []uint32
	send := func(down bool, key uint32) error {
		if down {
			return failing
		}
		released = append(released, key)
		return nil
	}

	err := playMacro(keyMacro{keys: []uint32{1, 2}, hold: time.Millisecond}, send, func(time.Duration) {})

	if !errors.Is(err, failing) {
		t.Errorf("err = %v, want %v", err, failing)
	}
	if !reflect.DeepEqual(released, []uint32{2, 1}) {
		t.Errorf("released %v, want [2 1]", released)
	}
}
