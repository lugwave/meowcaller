package meowcaller

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

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

// recoverHandler is deferred around the library's whatsmeow node handlers. It
// reports whether a panic was recovered, so a node handler can fall back to
// whatsmeow's own handling.
func (e *engine) recoverHandler(where string, panicked *bool) {
	r := recover()
	if r == nil {
		return
	}
	if panicked != nil {
		*panicked = true
	}
	e.logRecovered(where, r)
}

// recoverEvent is recoverHandler for the event handler. The event's type is
// only formatted when there was a panic — not on every event.
func (e *engine) recoverEvent(evt any) {
	r := recover()
	if r == nil {
		return
	}
	e.logRecovered(fmt.Sprintf("event %T", evt), r)
}

func (e *engine) logRecovered(where string, r any) {
	e.c.log.Error().
		Str("where", where).
		Str("panic", fmt.Sprint(r)).
		Str("stack", string(debug.Stack())).
		Msg("panic in call signaling handler recovered")
	// The handler may have panicked holding e.mu. Checked off the whatsmeow
	// goroutine: the probe waits up to lockProbeTimeout.
	go func() {
		if !e.engineLockFree() {
			e.logStuckLock(where)
		}
	}()
}

// lockProbeTimeout is how long the guards wait for e.mu after a panic before
// declaring it stuck (a time.Duration). Atomic so the tests can shorten it
// while a probe from an earlier test may still be running.
var lockProbeTimeout atomic.Int64

func init() { lockProbeTimeout.Store(int64(time.Second)) }

// engineLockFree reports whether e.mu can be taken within lockProbeTimeout.
// A goroutine that panicked while holding it leaves it held forever (most
// lock sections unlock without defer); the guards must find that out instead
// of blocking on it for good. Every section that parses network data does so
// before taking e.mu, so this should not happen — the probe is the backstop.
func (e *engine) engineLockFree() bool {
	deadline := time.Now().Add(time.Duration(lockProbeTimeout.Load()))
	for {
		if e.mu.TryLock() {
			e.mu.Unlock()
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *engine) logStuckLock(where string) {
	e.c.log.Error().
		Str("where", where).
		Dur("waited", time.Duration(lockProbeTimeout.Load())).
		Msg("engine lock still held after a panic: calls on this client are stuck — restart the host process (lock do engine preso após panic — reinicie a API)")
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
	// The goroutine that panicked may hold e.mu: probe it instead of blocking
	// on it forever (the call then stays as it is, and the log says why).
	if !e.engineLockFree() {
		e.logStuckLock("media " + callID)
		return
	}
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

// maskJID keeps only the last four characters of a JID's user part, so a log
// line at warning level identifies the device without carrying the number.
// "5511988887777@s.whatsapp.net" → "…7777@s.whatsapp.net".
func maskJID(jid string) string {
	user, server, found := strings.Cut(jid, "@")
	device := ""
	if i := strings.IndexAny(user, ":."); i >= 0 {
		user, device = user[:i], user[i:]
	}
	if len(user) > 4 {
		user = "…" + user[len(user)-4:]
	}
	if !found {
		return user + device
	}
	return user + device + "@" + server
}
