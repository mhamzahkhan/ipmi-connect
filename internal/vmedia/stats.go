package vmedia

import (
	"context"
	"os"
	"time"

	"ipmi-connect/internal/bmc"
)

// sample records transfer each interval and notices drives the BMC has dropped.
func (m *Manager) sample(ctx context.Context, sess Session) {
	t := time.NewTicker(m.sampleEvery)
	defer t.Stop()
	prev := map[uint16][2]int64{}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			prev = m.record(now, sess, prev)
		}
	}
}

// record takes one sample. The drive list is read under m.mu, so a map finishing at the same
// moment cannot be mistaken for a drive the BMC dropped.
func (m *Manager) record(now time.Time, sess Session, prev map[uint16][2]int64) map[uint16][2]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != sess {
		return prev
	}
	var read, written int64
	cur := map[uint16][2]int64{}
	for _, d := range sess.Drives() {
		cur[d.Disk] = [2]int64{d.BytesRead, d.BytesWritten}
		p := prev[d.Disk] // a drive mapped since the last sample starts from zero
		// A drive remapped within one interval restarts its counters: never count backwards.
		read += max(d.BytesRead-p[0], 0)
		written += max(d.BytesWritten-p[1], 0)
	}
	m.history = append(m.history, bmc.MediaSample{At: now.UnixMilli(), Read: read, Written: written})
	if over := len(m.history) - historyLen; over > 0 {
		m.history = append(m.history[:0:0], m.history[over:]...)
	}

	// A drive we think is attached but the session no longer lists was released by the BMC.
	var dropped []*os.File
	for t, mp := range m.drives {
		if _, ok := cur[mp.disk]; ok || mp.mounting {
			continue
		}
		if ev := sess.LastEvent(); ev != "" {
			m.lastErr = ev
		}
		dropped = append(dropped, mp.file)
		delete(m.drives, t)
	}
	for _, f := range dropped {
		if f != nil {
			_ = f.Close()
		}
	}
	if len(dropped) > 0 && len(m.drives) == 0 {
		go m.closeIfIdle()
	}
	return cur
}
