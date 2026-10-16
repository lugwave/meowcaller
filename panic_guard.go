package meowcaller

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/purpshell/meowcaller/signaling"
	waBinary "go.mau.fi/whatsmeow/binary"
)

// EndReasonMediaPanic is the OnEnd reason of a call whose media goroutine
// panicked: the library recovered, logged the stack and tore the call down.
const EndReasonMediaPanic = "media_panic"

// Panics in this library must not take the host process with them. A host
// usually runs many whatsmeow sessions in one process — a panic on any
// goroutine Go does not recover kills every one of them, not just the call.
// whatsmeow already recovers panics in event handlers, but not in node
// handlers (the <ack>/<call> hook below runs on a bare goroutine of its
// handler queue), and nothing recovers the goroutines the media path starts.
//
// The guards below are what every such goroutine defers. They log the panic
// with its stack at error level and end the affected call; they never
// re-panic.

// recoverMedia is deferred by every goroutine that serves one call's media. On
// a panic the call can no longer carry audio, so it is ended — on a fresh
// goroutine (abortAfterPanic), because the goroutine that panicked may have
// been holding e.mu, and the guard must not deadlock on it.
func (e *engine) recoverMedia(where, callID string) {
	r := recover()
	if r == nil {
		return
	}
	e.c.log.Error().
		Str("call_id", callID).
		Str("where", where).
		Str("panic", fmt.Sprint(r)).
		Str("stack", string(debug.Stack())).
		Msg("panic in call media goroutine recovered; ending the call")
	go e.abortAfterPanic(callID)
}

// recoverHandler is deferred around the library's whatsmeow hooks (event
// handler and node handlers). It reports whether a panic was recovered, so a
// node handler can fall back to whatsmeow's own handling.
func (e *engine) recoverHandler(where string, panicked *bool) {
	r := recover()
	if r == nil {
		return
	}
	if panicked != nil {
		*panicked = true
	}
	e.c.log.Error().
		Str("where", where).
		Str("panic", fmt.Sprint(r)).
		Str("stack", string(debug.Stack())).
		Msg("panic in call signaling handler recovered")
}

// abortAfterPanic ends a call whose media died with a panic.
//
// A call this device has not engaged in — an inbound call still ringing here
// (with early media, media runs from the offer) or a group invite in the
// waiting room — ends locally, like EndLocal: it may still be ringing on the
// account's other devices, and a <reject> or <terminate> would act on it
// there. A call this device answered, and every outbound call, is terminated
// on the wire as well: ending it only here would leave the peer on a silent
// line, or the callee's phone ringing for nobody.
func (e *engine) abortAfterPanic(callID string) {
	defer func() {
		if r := recover(); r != nil {
			e.c.log.Error().Str("call_id", callID).Str("panic", fmt.Sprint(r)).
				Msg("panic while ending a call after a media panic")
		}
	}()
	e.mu.Lock()
	m := e.calls[callID]
	var call *Call
	outgoing := false
	if m != nil {
		call = m.call
		outgoing = m.direction == CallDirectionOutgoing
	}
	e.mu.Unlock()
	if call == nil || !terminateAfterPanic(call.State(), outgoing) {
		e.finishCall(callID, EndReasonMediaPanic)
		return
	}
	to, creator := call.peer, call.peer
	if m != nil {
		e.mu.Lock()
		to, creator = m.from, m.creator
		e.mu.Unlock()
	}
	term := signaling.BuildTerminate(&signaling.TerminateParams{CallID: callID, To: to, CallCreator: creator})
	term.Attrs["id"] = e.nextCallNodeID()
	e.finishCall(callID, EndReasonMediaPanic)
	if err := e.transmitCallNode(context.Background(), term); err != nil {
		e.c.log.Warn().Err(err).Str("call_id", callID).Msg("send terminate after media panic failed")
	}
}

// terminateAfterPanic reports whether a call in this phase has to be ended on
// the wire after a media panic (see abortAfterPanic).
func terminateAfterPanic(phase CallPhase, outgoing bool) bool {
	switch phase {
	case CallPhaseEnded:
		return false
	case CallPhaseRinging, CallPhaseWaitingRoom:
		return outgoing
	}
	return true
}

// onCallRawGuarded is onCallRaw behind a recover. A <call> node whose raw
// handling panicked is reported as not handled, so whatsmeow's own handler
// still acks it and turns it into the usual event: the host keeps seeing the
// call even when this library choked on it.
func (e *engine) onCallRawGuarded(node *waBinary.Node) (handled bool) {
	panicked := false
	defer func() {
		if panicked {
			handled = false
		}
	}()
	defer e.recoverHandler("call node", &panicked)
	return e.onCallRaw(node)
}
