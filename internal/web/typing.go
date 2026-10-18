package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"ipmi-connect/internal/textkeys"
)

const (
	// typeKeyDelay paces typed key events: BIOS and boot loaders drop faster input.
	typeKeyDelay = 12 * time.Millisecond
	maxTypeRunes = 16 << 10
)

// typeText types text on the host as US-layout key presses, one paste at a time. It returns how
// many characters it typed and the ones it could not. On cancel it stops after releasing the
// keys it was holding.
func (s *Server) typeText(ctx context.Context, text string) (typed int, skipped string, err error) {
	if utf8.RuneCountInString(text) > maxTypeRunes {
		return 0, "", fmt.Errorf("the text is longer than %d characters", maxTypeRunes)
	}
	if s.Console == nil {
		return 0, "", errors.New("BMC console not connected")
	}
	if !s.typeMu.TryLock() {
		return 0, "", errors.New("already typing a paste; wait for it to finish or cancel it")
	}
	defer s.typeMu.Unlock()

	keys, skipped := textkeys.Encode(text)
	log.Printf("[Input] Typing %d keys", len(keys))
	send := func(down bool, usage uint16) error {
		if err := s.Console.SendKeyEvent(down, usage); err != nil {
			return err
		}
		if s.typeDelay > 0 {
			select {
			case <-time.After(s.typeDelay):
			case <-ctx.Done():
			}
		}
		return nil
	}
	for _, k := range keys {
		if ctx.Err() != nil {
			return typed, skipped, errors.New("typing cancelled")
		}
		if err := typeKey(send, k); err != nil {
			return typed, skipped, err
		}
		typed++
	}
	return typed, skipped, nil
}

// typeKey presses and releases one key, with Shift around it when needed. Releases are always
// attempted, so a key is never left held.
func typeKey(send func(bool, uint16) error, k textkeys.Key) error {
	if k.Shift {
		if err := send(true, textkeys.LeftShift); err != nil {
			return err
		}
		defer func() { _ = send(false, textkeys.LeftShift) }()
	}
	if err := send(true, k.Usage); err != nil {
		return err
	}
	return send(false, k.Usage)
}

// handleTyping starts or cancels a paste for one browser: {"type":"type","text":...} and
// {"type":"type-cancel"}. The result goes back as {"type":"typed","count":n,"skipped":...,"error":...}.
// Other messages are left to inputState.
func (s *Server) handleTyping(ctx context.Context, client *wsClient, in *inputState, data []byte, cancel *context.CancelFunc) {
	var msg clientMessage
	if json.Unmarshal(data, &msg) != nil {
		return
	}
	switch msg.Type {
	case "type-cancel":
		if *cancel != nil {
			(*cancel)()
		}
	case "type":
		if in.typing.Load() {
			s.reply(client, map[string]any{"type": "typed", "error": "already typing a paste"})
			return
		}
		in.releaseAll() // keys held in the browser would modify the typed text
		tctx, tcancel := context.WithCancel(ctx)
		*cancel = tcancel
		in.typing.Store(true)
		go func() {
			defer tcancel()
			n, skipped, err := s.typeText(tctx, msg.Text)
			in.typing.Store(false)
			res := map[string]any{"type": "typed", "count": n, "skipped": skipped}
			if err != nil {
				res["error"] = err.Error()
			}
			s.reply(client, res)
		}()
	}
}

// reply queues a JSON message for one browser without blocking.
func (s *Server) reply(client *wsClient, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case client.out <- outMessage{typ: websocket.MessageText, data: data}:
	default:
	}
}
