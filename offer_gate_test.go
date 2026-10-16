package meowcaller

import (
	"errors"
	"testing"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func gatedOfferEngine(gate func(Offer) bool) (*engine, *int) {
	c := &Client{offerGate: gate}
	c.eng = newEngine(c)
	fired := 0
	c.OnIncomingCall(func(*Call) { fired++ })
	return c.eng, &fired
}

// A refused offer must leave no trace: no call state, no listener, nothing that
// would lead to a <preaccept>.
func TestOfferGateRefusalDoesNotEngage(t *testing.T) {
	var seen Offer
	eng, fired := gatedOfferEngine(func(o Offer) bool { seen = o; return false })

	eng.onOffer(&events.CallOffer{
		BasicCallMeta: types.BasicCallMeta{CallID: "CID", From: peerJID(), CallCreator: creatorJID()},
		Data:          &waBinary.Node{Tag: "offer", Attrs: waBinary.Attrs{"call-id": "CID"}},
	})

	if seen.CallID != "CID" || seen.From != peerJID() || seen.Creator != creatorJID() || seen.Group {
		t.Fatalf("gate saw %+v", seen)
	}
	if eng.lookup("CID") != nil {
		t.Fatal("a refused offer created call state")
	}
	if *fired != 0 {
		t.Fatal("OnIncomingCall fired for a refused offer")
	}
}

// The check a consumer runs after a whatsmeow update: the hook must install on a
// real client, and a failed install must be visible.
func TestHookErrorReflectsTheInstall(t *testing.T) {
	wa := whatsmeow.NewClient(&store.Device{}, waLog.Noop)
	if err := NewClient(wa).HookError(); err != nil {
		t.Fatalf("hook did not install on a whatsmeow client: %v", err)
	}

	broken := &Client{}
	broken.eng = newEngine(broken)
	broken.eng.rawCallHookErr = errors.New("layout changed")
	if broken.HookError() == nil {
		t.Fatal("a failed install was reported as working")
	}
}
