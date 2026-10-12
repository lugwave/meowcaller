package meowcaller

import (
	"context"
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// This account in the tests: a linked device (device 7) of one user, known by its
// phone number and by its LID. The primary phone is device 0 of the same user.
func ownPN() types.JID {
	return types.JID{User: "5511900000000", Server: types.DefaultUserServer, Device: 7}
}
func ownLID() types.JID {
	return types.JID{User: "333333333333333", Server: types.HiddenUserServer, Device: 7}
}

// inboundRingingEngine is an inbound 1:1 call with media already up (as it is since
// the offer), on a client that knows its own JIDs. sent counts stanzas put on the
// wire; canceled reports that the media context was canceled.
func inboundRingingEngine() (eng *engine, call *Call, sent *int, canceled *bool, reasons *[]string) {
	eng, call, _ = testEngineWithIncomingCall()
	eng.c.ownGroupJIDs = func() []types.JID { return []types.JID{ownPN(), ownLID()} }
	sent, canceled, reasons = new(int), new(bool), new([]string)
	eng.sendCallNode = func(context.Context, waBinary.Node) error { *sent++; return nil }
	eng.calls["CID"].started = true
	eng.calls["CID"].cancel = func() { *canceled = true }
	call.OnEnd(func(r string) { *reasons = append(*reasons, r) })
	return eng, call, sent, canceled, reasons
}

func acceptFrom(from types.JID) *events.CallAccept {
	return &events.CallAccept{BasicCallMeta: types.BasicCallMeta{
		CallID: "CID", From: from, CallCreator: creatorJID(),
	}}
}

// EndLocal tears the call down here and says nothing to anyone.
func TestEndLocalTearsDownWithoutSignaling(t *testing.T) {
	eng, call, sent, canceled, reasons := inboundRingingEngine()

	if err := call.EndLocal("ring_timeout"); err != nil {
		t.Fatalf("EndLocal: %v", err)
	}
	if *sent != 0 {
		t.Fatalf("EndLocal put %d stanza(s) on the wire, want none", *sent)
	}
	if !*canceled {
		t.Fatal("EndLocal did not cancel local media")
	}
	if call.State() != CallPhaseEnded || eng.lookup("CID") != nil {
		t.Fatalf("after EndLocal: phase=%d, still registered=%v", call.State(), eng.lookup("CID") != nil)
	}
	if len(*reasons) != 1 || (*reasons)[0] != "ring_timeout" {
		t.Fatalf("OnEnd reasons = %v, want [ring_timeout]", *reasons)
	}

	// Idempotent, and a late <terminate> for the same call changes nothing.
	if err := call.EndLocal("again"); err != nil {
		t.Fatalf("second EndLocal: %v", err)
	}
	eng.onTerminate("CID", "timeout")
	if len(*reasons) != 1 || *sent != 0 {
		t.Fatalf("after a second end: reasons=%v sent=%d, want one reason and nothing sent", *reasons, *sent)
	}
}

func TestEndLocalDefaultsTheReason(t *testing.T) {
	_, call, _, _, reasons := inboundRingingEngine()
	_ = call.EndLocal("")
	if len(*reasons) != 1 || (*reasons)[0] != "local" {
		t.Fatalf("OnEnd reasons = %v, want [local]", *reasons)
	}
}

// EndLocal also ends an outgoing call locally without a <terminate>.
func TestEndLocalOnOutgoingCallSendsNothing(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	sent := 0
	eng.sendCallNode = func(context.Context, waBinary.Node) error { sent++; return nil }
	_ = call.EndLocal("drop")
	if sent != 0 || call.State() != CallPhaseEnded {
		t.Fatalf("sent=%d phase=%d, want nothing sent and Ended", sent, call.State())
	}
}

// The <accept> of an inbound call coming from another device of this account (the
// paired phone answered) ends the call here, locally: media stops, nothing is sent.
func TestInboundAcceptFromOwnOtherDeviceEndsLocally(t *testing.T) {
	for name, from := range map[string]types.JID{
		"primary phone, by phone number": {User: ownPN().User, Server: types.DefaultUserServer},
		"primary phone, by LID":          {User: ownLID().User, Server: types.HiddenUserServer},
		"another linked device":          {User: ownLID().User, Server: types.HiddenUserServer, Device: 12},
	} {
		eng, call, sent, canceled, reasons := inboundRingingEngine()

		eng.onAccept(acceptFrom(from))

		if call.State() != CallPhaseEnded || eng.lookup("CID") != nil || !*canceled {
			t.Fatalf("%s: phase=%d registered=%v canceled=%v, want ended and media canceled",
				name, call.State(), eng.lookup("CID") != nil, *canceled)
		}
		if len(*reasons) != 1 || (*reasons)[0] != "accepted_elsewhere" {
			t.Fatalf("%s: OnEnd reasons = %v, want [accepted_elsewhere]", name, *reasons)
		}
		if *sent != 0 {
			t.Fatalf("%s: %d stanza(s) sent, want none — the call belongs to the device that answered", name, *sent)
		}
	}
}

// Any other <accept> leaves an inbound call exactly as it was.
func TestInboundAcceptThatIsNotOwnOtherDeviceIsIgnored(t *testing.T) {
	stranger := types.JID{User: "444444444444444", Server: types.HiddenUserServer, Device: 3}
	cases := map[string]struct {
		from  types.JID
		setup func(*engine)
	}{
		"the caller":             {from: peerJID()},
		"someone else":           {from: stranger},
		"this very device (PN)":  {from: ownPN()},
		"this very device (LID)": {from: ownLID()},
		"no sender":              {from: types.JID{}},
		"a group call": {
			from:  types.JID{User: ownLID().User, Server: types.HiddenUserServer},
			setup: func(e *engine) { e.calls["CID"].group = true },
		},
		"a call this device answered": {
			from:  types.JID{User: ownLID().User, Server: types.HiddenUserServer},
			setup: func(e *engine) { e.calls["CID"].answered = true },
		},
		"own JIDs unknown": {
			from:  types.JID{User: ownLID().User, Server: types.HiddenUserServer},
			setup: func(e *engine) { e.c.ownGroupJIDs = nil },
		},
	}
	for name, tc := range cases {
		eng, call, sent, canceled, reasons := inboundRingingEngine()
		if tc.setup != nil {
			tc.setup(eng)
		}

		eng.onAccept(acceptFrom(tc.from))

		if call.State() != CallPhaseRinging || eng.lookup("CID") == nil || *canceled || len(*reasons) != 0 || *sent != 0 {
			t.Fatalf("%s: phase=%d registered=%v canceled=%v reasons=%v sent=%d, want the call untouched",
				name, call.State(), eng.lookup("CID") != nil, *canceled, *reasons, *sent)
		}
	}
}

// The outgoing path is untouched: an <accept> from this account's own other device
// on a call WE placed is still "the peer answered", never a local end.
func TestOutgoingAcceptIsNeverAnAcceptElsewhere(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	eng.c.ownGroupJIDs = func() []types.JID { return []types.JID{ownPN(), ownLID()} }
	accepted := false
	call.OnPeerAccept(func() { accepted = true })

	eng.onAccept(acceptFrom(types.JID{User: ownLID().User, Server: types.HiddenUserServer}))

	if call.State() == CallPhaseEnded || eng.lookup("CID") == nil {
		t.Fatal("an accept on an outgoing call ended it")
	}
	if !accepted {
		t.Fatal("the outgoing accept was not delivered to OnPeerAccept")
	}
}
