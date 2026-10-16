package meowcaller

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types/events"
)

// lockedBuffer is a log sink safe across the goroutines the guards spawn.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func shortLockProbe(t *testing.T) {
	t.Helper()
	old := lockProbeTimeout.Swap(int64(50 * time.Millisecond))
	t.Cleanup(func() { lockProbeTimeout.Store(old) })
}

// A media goroutine that panicked while holding e.mu leaves it held. The guard
// must not block on it forever: it gives up after the probe, says so at error
// level, and leaves the call as it is.
func TestAbortAfterPanicWithTheEngineLockStuckGivesUp(t *testing.T) {
	shortLockProbe(t)
	eng, call, _ := testEngineWithIncomingCall()
	logs := &lockedBuffer{}
	eng.c.log = zerolog.New(logs)

	eng.mu.Lock() // the goroutine that "panicked" never unlocked
	done := make(chan struct{})
	go func() {
		eng.abortAfterPanic("CID")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("abortAfterPanic blocked on a stuck engine lock")
	}
	eng.mu.Unlock()

	if !strings.Contains(logs.String(), "engine lock still held after a panic") {
		t.Fatalf("the stuck lock was not reported: %s", logs.String())
	}
	if call.State() != CallPhaseRinging {
		t.Fatalf("phase = %d; with the lock stuck the call is left as it is", call.State())
	}
}

// The handler guards probe the lock too, off the whatsmeow goroutine.
func TestHandlerPanicWithTheEngineLockStuckIsReported(t *testing.T) {
	shortLockProbe(t)
	eng, _, _ := testEngineWithIncomingCall()
	logs := &lockedBuffer{}
	eng.c.log = zerolog.New(logs)

	eng.mu.Lock()
	func() {
		defer eng.recoverEvent(&events.CallOffer{})
		panic("parser blew up while holding e.mu")
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logs.String(), "engine lock still held") {
		if time.Now().After(deadline) {
			t.Fatalf("the stuck lock was not reported: %s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	eng.mu.Unlock()
	if !strings.Contains(logs.String(), "event *events.CallOffer") {
		t.Fatalf("the event type was not in the panic report: %s", logs.String())
	}
}

// With the lock free the probe stays quiet.
func TestHandlerPanicWithTheEngineLockFreeReportsOnlyThePanic(t *testing.T) {
	shortLockProbe(t)
	eng, _, _ := testEngineWithIncomingCall()
	logs := &lockedBuffer{}
	eng.c.log = zerolog.New(logs)

	func() {
		defer eng.recoverEvent(&events.CallOffer{})
		panic("boom")
	}()
	time.Sleep(150 * time.Millisecond)
	if strings.Contains(logs.String(), "engine lock still held") {
		t.Fatalf("a free lock was reported as stuck: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "panic in call signaling handler recovered") {
		t.Fatalf("the panic was not logged: %s", logs.String())
	}
}

func TestMaskJID(t *testing.T) {
	for in, want := range map[string]string{
		"5511988887777@s.whatsapp.net":     "…7777@s.whatsapp.net",
		"222222222222222@lid":              "…2222@lid",
		"222222222222222:7@lid":            "…2222:7@lid",
		"5511988887777.0:3@s.whatsapp.net": "…7777.0:3@s.whatsapp.net",
		"123@lid":                          "123@lid",
		"":                                 "",
	} {
		if got := maskJID(in); got != want {
			t.Errorf("maskJID(%q) = %q, want %q", in, got, want)
		}
	}
}
