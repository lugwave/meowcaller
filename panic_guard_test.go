package meowcaller

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	waBinary "go.mau.fi/whatsmeow/binary"
)

// panicProbe records what a call did on its way down: the OnEnd reasons and the
// stanzas put on the wire. Safe across the goroutine the guards spawn.
type panicProbe struct {
	mu      sync.Mutex
	reasons []string
	sent    []string
	ended   chan struct{}
}

func probeCall(eng *engine, call *Call) *panicProbe {
	p := &panicProbe{ended: make(chan struct{}, 4)}
	eng.sendCallNode = func(_ context.Context, n waBinary.Node) error {
		p.mu.Lock()
		p.sent = append(p.sent, n.Tag)
		p.mu.Unlock()
		return nil
	}
	call.OnEnd(func(r string) {
		p.mu.Lock()
		p.reasons = append(p.reasons, r)
		p.mu.Unlock()
		p.ended <- struct{}{}
	})
	return p
}

func (p *panicProbe) snapshot() (reasons, sent []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.reasons...), append([]string(nil), p.sent...)
}

// A panic on a media goroutine is recovered (the test binary would die
// otherwise) and the call it served is ended with EndReasonMediaPanic.
func TestRecoverMediaKeepsTheProcessAndEndsTheCall(t *testing.T) {
	eng, call, _ := testEngineWithIncomingCall()
	p := probeCall(eng, call)

	go func() {
		defer eng.recoverMedia("test", "CID")
		var m map[string]int
		m["boom"]++ // a nil-map write: a real runtime panic, not a test double
	}()

	select {
	case <-p.ended:
	case <-time.After(2 * time.Second):
		t.Fatal("the call was not ended after its media goroutine panicked")
	}
	reasons, _ := p.snapshot()
	if len(reasons) != 1 || reasons[0] != EndReasonMediaPanic {
		t.Fatalf("OnEnd reasons = %v, want [%s]", reasons, EndReasonMediaPanic)
	}
	if call.State() != CallPhaseEnded || eng.lookup("CID") != nil {
		t.Fatalf("after the panic: phase=%d registered=%v", call.State(), eng.lookup("CID") != nil)
	}
}

// What goes on the wire after a media panic depends on whether this device is
// in the call: an inbound call still ringing here ends locally (it may be
// ringing on the account's other devices); one answered here, and any outbound
// call, is terminated so the peer is not left on a silent line.
func TestAbortAfterPanicEndsOnTheWireOnlyWhatThisDeviceIsIn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		outgoing bool
		phase    CallPhase
		wantSent []string
	}{
		{"inbound ringing", false, CallPhaseRinging, nil},
		{"inbound answered, connecting", false, CallPhaseConnecting, []string{"call"}},
		{"inbound answered, active", false, CallPhaseActive, []string{"call"}},
		{"outbound calling", true, CallPhaseCalling, []string{"call"}},
		{"outbound ringing", true, CallPhaseRinging, []string{"call"}},
		{"outbound active", true, CallPhaseActive, []string{"call"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var eng *engine
			var call *Call
			if tc.outgoing {
				eng, call = testEngineWithOutgoingCall()
			} else {
				eng, call, _ = testEngineWithIncomingCall()
			}
			call.phase = tc.phase
			p := probeCall(eng, call)

			eng.abortAfterPanic("CID")

			reasons, sent := p.snapshot()
			if len(reasons) != 1 || reasons[0] != EndReasonMediaPanic {
				t.Fatalf("OnEnd reasons = %v, want [%s]", reasons, EndReasonMediaPanic)
			}
			if len(sent) != len(tc.wantSent) {
				t.Fatalf("stanzas sent = %v, want %v", sent, tc.wantSent)
			}
			if call.State() != CallPhaseEnded || eng.lookup("CID") != nil {
				t.Fatalf("phase=%d registered=%v, want ended and gone", call.State(), eng.lookup("CID") != nil)
			}
		})
	}
}

// A call that is already gone is left alone: no stanza, no second OnEnd.
func TestAbortAfterPanicOnAnEndedCallDoesNothing(t *testing.T) {
	eng, call, _ := testEngineWithIncomingCall()
	call.phase = CallPhaseActive
	p := probeCall(eng, call)
	eng.finishCall("CID", "hangup")
	<-p.ended

	eng.abortAfterPanic("CID")
	reasons, sent := p.snapshot()
	if len(reasons) != 1 || reasons[0] != "hangup" || len(sent) != 0 {
		t.Fatalf("after abort on an ended call: reasons=%v sent=%v", reasons, sent)
	}
}

// The <call> node hook falls back to whatsmeow's own handling when the raw
// handling panics, instead of killing the handler-queue goroutine.
func TestCallNodeHookRecoversAndFallsBack(t *testing.T) {
	eng, _, _ := testEngineWithIncomingCall()
	if eng.onCallRawGuarded(nil) {
		t.Fatal("a <call> node whose handling panicked was reported as handled")
	}
}

// A relay reader that panics counts as that relay's reader dying: with it the
// last one, the fanout closes and Recv reports it instead of blocking forever.
func TestFanoutReaderPanicDropsOnlyThatRelay(t *testing.T) {
	f := &relayFanout{
		packets: make(chan []byte, 1),
		done:    make(chan struct{}),
		live:    2,
		log:     zerolog.Nop(),
	}
	f.guardedReadLoop(nil) // nil channel: Recv dereferences it and panics
	select {
	case <-f.done:
		t.Fatal("one dead reader out of two closed the fanout")
	default:
	}
	f.guardedReadLoop(nil)
	if _, err := f.Recv(make([]byte, 16)); err != errFanoutClosed {
		t.Fatalf("Recv after every reader died = %v, want errFanoutClosed", err)
	}
}
