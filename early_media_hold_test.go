package meowcaller

import (
	"context"
	"crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/purpshell/meowcaller/relay"
	waBinary "go.mau.fi/whatsmeow/binary"
)

// lugwave (experimental): tests of WithInboundEarlyMedia — what an inbound 1:1
// call sends to the relay before Answer in each EarlyMediaMode. The media loop
// runs for real (relay transport, allocate, MLow, SRTP/SRTCP) against a local
// relay speaking the same transport stack (fakeRelay, relay_fanout_test.go); what
// these tests cannot say is how WhatsApp's relays or the caller's phone react.

func TestEarlyMediaModeNames(t *testing.T) {
	for mode, want := range map[EarlyMediaMode]string{
		EarlyMediaFull: "full", EarlyMediaHoldRTP: "hold_rtp",
		EarlyMediaHoldRTPAndRTCP: "hold_rtp_rtcp", EarlyMediaOff: "off",
		EarlyMediaMode(99): "unknown",
	} {
		if got := mode.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", mode, got, want)
		}
	}
	if EarlyMediaFull.holdsOutbound() || EarlyMediaOff.holdsOutbound() ||
		!EarlyMediaHoldRTP.holdsOutbound() || !EarlyMediaHoldRTPAndRTCP.holdsOutbound() {
		t.Fatal("holdsOutbound: only the two hold modes hold the outbound audio")
	}
}

func TestWithInboundEarlyMediaReachesTheClient(t *testing.T) {
	var asked Offer
	cfg := resolveConfig([]Option{WithInboundEarlyMedia(func(o Offer) EarlyMediaMode {
		asked = o
		return EarlyMediaHoldRTPAndRTCP
	})})
	if cfg.earlyMedia == nil {
		t.Fatal("WithInboundEarlyMedia did not set the config")
	}
	if got := cfg.earlyMedia(Offer{CallID: "CID"}); got != EarlyMediaHoldRTPAndRTCP || asked.CallID != "CID" {
		t.Fatalf("earlyMedia = %v (asked %q)", got, asked.CallID)
	}
}

// relayTally counts, by kind, what one fake relay got from the library.
type relayTally struct {
	mu                   sync.Mutex
	stun, rtp, rtcp, oth int
}

func (r *relayTally) snapshot() (stun, rtp, rtcp int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stun, r.rtp, r.rtcp
}

func tallyRelay(fr *fakeRelay, stop <-chan struct{}) *relayTally {
	tl := &relayTally{}
	go func() {
		for {
			select {
			case <-stop:
				return
			case p := <-fr.got:
				tl.mu.Lock()
				switch relay.ClassifyRelayPacket(p) {
				case relay.RelayPacketStun:
					tl.stun++
				case relay.RelayPacketRtp:
					tl.rtp++
				case relay.RelayPacketRtcp:
					tl.rtcp++
				default:
					tl.oth++
				}
				tl.mu.Unlock()
			}
		}
	}()
	return tl
}

// earlyMediaEngine is an inbound 1:1 call, ringing, in mode, whose offer carried a
// relay pointing at fr and a callKey — the state onOffer leaves before it calls
// maybeStartMedia. Stanzas go to a counter (the fallback accept).
func earlyMediaEngine(t *testing.T, fr *fakeRelay, mode EarlyMediaMode) (*engine, *Call, *int) {
	t.Helper()
	eng, call, _ := testEngineWithIncomingCall()
	var mu sync.Mutex
	sent := new(int)
	eng.sendCallNode = func(context.Context, waBinary.Node) error {
		mu.Lock()
		*sent++
		mu.Unlock()
		return nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	m := eng.calls["CID"]
	m.callKey = key
	m.selfLID = "333333333333333:7@lid"
	m.peerLID = "222222222222222@lid"
	m.relay = &relayData{
		relayKeyASCII: []byte("relay-key"),
		relayTokens:   [][]byte{[]byte("token-0")},
		endpoints: []relayEndpoint{{
			relayName: "local", tokenID: 0,
			addresses: []relayAddress{{ipv4: "127.0.0.1", port: uint16(fr.addr.Port)}},
		}},
	}
	m.earlyMode = mode
	m.offerAt = time.Now()
	if mode.holdsOutbound() {
		m.txRelease = make(chan struct{})
	}
	t.Cleanup(func() { eng.finishCall("CID", "test_done") })
	return eng, call, sent
}

// ringThenAnswer brings media up as onOffer does and lets the call ring for d (longer
// than the 1.5 s SRTCP period), then returns what the relay got.
func ringThenAnswer(t *testing.T, mode EarlyMediaMode) (before, after [3]int, relayUp bool) {
	t.Helper()
	fr := startFakeRelay(t)
	stop := make(chan struct{})
	defer close(stop)
	tl := tallyRelay(fr, stop)
	eng, call, _ := earlyMediaEngine(t, fr, mode)

	eng.maybeStartMedia("CID")
	time.Sleep(2200 * time.Millisecond)
	before[0], before[1], before[2] = tl.snapshot()
	select {
	case <-fr.ready:
		relayUp = true
	default:
	}

	if err := eng.answer(call); err != nil {
		t.Fatalf("answer: %v", err)
	}
	time.Sleep(2200 * time.Millisecond)
	s, r, c := tl.snapshot()
	after = [3]int{s - before[0], r - before[1], c - before[2]}
	return before, after, relayUp
}

// The default (upstream #43) sends audio RTP and SRTCP while ringing — the packets
// the caller's phone counts as a connected call.
func TestEarlyMediaFullSendsAudioWhileRinging(t *testing.T) {
	t.Parallel()
	before, after, up := ringThenAnswer(t, EarlyMediaFull)
	if !up || before[0] == 0 {
		t.Fatalf("relay up=%v stun=%d: the transport and the allocate must start on the offer", up, before[0])
	}
	if before[1] == 0 || before[2] == 0 {
		t.Fatalf("while ringing rtp=%d rtcp=%d: the default sends both", before[1], before[2])
	}
	if after[1] == 0 {
		t.Fatal("no RTP after Answer")
	}
}

// Variant B: the transport, the allocate+ping keepalive and the SRTCP sender
// reports start on the offer; audio RTP only after Answer.
func TestEarlyMediaHoldRTPSendsNoAudioUntilAnswer(t *testing.T) {
	t.Parallel()
	before, after, up := ringThenAnswer(t, EarlyMediaHoldRTP)
	if !up || before[0] < 2 {
		t.Fatalf("relay up=%v stun=%d: transport, allocate and ping must start on the offer", up, before[0])
	}
	if before[1] != 0 {
		t.Fatalf("%d RTP packet(s) sent while ringing, want none", before[1])
	}
	if before[2] == 0 {
		t.Fatal("hold_rtp keeps the SRTCP sender reports while ringing; none was sent")
	}
	if after[1] == 0 || after[2] == 0 {
		t.Fatalf("after Answer rtp=%d rtcp=%d: both must flow", after[1], after[2])
	}
}

// Variant A: only the transport and the allocate+ping keepalive while ringing;
// neither audio RTP nor SRTCP until Answer, both right after.
func TestEarlyMediaHoldRTPAndRTCPSendsOnlySTUNUntilAnswer(t *testing.T) {
	t.Parallel()
	before, after, up := ringThenAnswer(t, EarlyMediaHoldRTPAndRTCP)
	if !up || before[0] < 2 {
		t.Fatalf("relay up=%v stun=%d: transport, allocate and ping must start on the offer", up, before[0])
	}
	if before[1] != 0 || before[2] != 0 {
		t.Fatalf("while ringing rtp=%d rtcp=%d, want none of either", before[1], before[2])
	}
	if after[1] == 0 || after[2] == 0 {
		t.Fatalf("after Answer rtp=%d rtcp=%d: both must flow", after[1], after[2])
	}
}

// Variant C (control, the behavior before #43): nothing reaches the relay before
// Answer — not even the transport; media starts on Answer.
func TestEarlyMediaOffStartsNothingUntilAnswer(t *testing.T) {
	t.Parallel()
	before, after, up := ringThenAnswer(t, EarlyMediaOff)
	if up || before != [3]int{} {
		t.Fatalf("while ringing relay up=%v got stun/rtp/rtcp=%v, want nothing", up, before)
	}
	if after[0] == 0 || after[1] == 0 {
		t.Fatalf("after Answer stun/rtp/rtcp=%v: media must start on Answer", after)
	}
}

// A relaylatency/transport carrying relay data reaches maybeStartMedia too
// (onRelay); EarlyMediaOff must refuse it while ringing, like the offer.
func TestEarlyMediaOffRefusesRelayStartWhileRinging(t *testing.T) {
	eng, _, _ := testEngineWithIncomingCall()
	m := eng.calls["CID"]
	m.earlyMode = EarlyMediaOff
	m.callKey = []byte("k")
	m.relay = &relayData{}
	eng.maybeStartMedia("CID")
	if m.started {
		t.Fatal("EarlyMediaOff started media before Answer")
	}
}

// Answer releases the hold once; a second Answer must not close the channel again.
func TestEarlyMediaAnswerReleasesTheHoldOnce(t *testing.T) {
	old := acceptFallbackDelay
	acceptFallbackDelay = time.Hour
	defer func() { acceptFallbackDelay = old }()

	eng, call, _ := testEngineWithIncomingCall()
	m := eng.calls["CID"]
	m.earlyMode = EarlyMediaHoldRTPAndRTCP
	m.txRelease = make(chan struct{})
	if err := eng.answer(call); err != nil {
		t.Fatalf("answer: %v", err)
	}
	select {
	case <-m.txRelease:
	default:
		t.Fatal("Answer did not release the held audio")
	}
	if !m.txReleased || m.answeredAt.IsZero() {
		t.Fatalf("txReleased=%v answeredAt=%v", m.txReleased, m.answeredAt)
	}
	if err := eng.answer(call); err != nil { // would panic on a double close
		t.Fatalf("second answer: %v", err)
	}
}

func muteV2Node() *waBinary.Node {
	return &waBinary.Node{
		Tag:   "call",
		Attrs: waBinary.Attrs{"from": peerJID(), "id": "N1"},
		Content: []waBinary.Node{{Tag: "mute_v2", Attrs: waBinary.Attrs{
			"call-id": "CID", "call-creator": creatorJID(), "mute-state": "0",
		}}},
	}
}

// acceptCounter counts the <accept> stanzas put on the wire.
func acceptCounter(eng *engine) func() int {
	var mu sync.Mutex
	n := 0
	eng.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		for _, kid := range node.GetChildren() {
			if kid.Tag == "accept" {
				mu.Lock()
				n++
				mu.Unlock()
			}
		}
		return nil
	}
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// With the audio held the caller's mute_v2 comes after Answer (the pre-#43
// order) and the accept goes out from it — once, the fallback stays quiet.
func TestEarlyMediaHoldAcceptsOnMuteV2AfterAnswer(t *testing.T) {
	old := acceptFallbackDelay
	acceptFallbackDelay = 50 * time.Millisecond
	defer func() { acceptFallbackDelay = old }()

	eng, call, _ := testEngineWithIncomingCall()
	accepts := acceptCounter(eng)
	m := eng.calls["CID"]
	m.earlyMode = EarlyMediaHoldRTPAndRTCP
	m.txRelease = make(chan struct{})
	if err := eng.answer(call); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if accepts() != 0 {
		t.Fatal("accept sent before the caller's mute_v2")
	}
	eng.onCallRaw(muteV2Node())
	if accepts() != 1 {
		t.Fatalf("accepts after mute_v2 = %d, want 1", accepts())
	}
	time.Sleep(150 * time.Millisecond)
	if accepts() != 1 {
		t.Fatalf("accepts after the fallback delay = %d, want still 1", accepts())
	}
}

// No mute_v2 at all: the fallback sends the accept, once.
func TestEarlyMediaHoldFallbackAcceptsWithoutMuteV2(t *testing.T) {
	old := acceptFallbackDelay
	acceptFallbackDelay = 50 * time.Millisecond
	defer func() { acceptFallbackDelay = old }()

	eng, call, _ := testEngineWithIncomingCall()
	accepts := acceptCounter(eng)
	m := eng.calls["CID"]
	m.earlyMode = EarlyMediaHoldRTP
	m.txRelease = make(chan struct{})
	if err := eng.answer(call); err != nil {
		t.Fatalf("answer: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for accepts() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if accepts() != 1 {
		t.Fatalf("fallback accepts = %d, want 1", accepts())
	}
	eng.onCallRaw(muteV2Node()) // a late mute_v2 must not send a second accept
	time.Sleep(100 * time.Millisecond)
	if accepts() != 1 {
		t.Fatalf("accepts after a late mute_v2 = %d, want 1", accepts())
	}
}

// The default mode keeps its behavior: no fallback — without a mute_v2 the accept
// waits for it, as before.
func TestEarlyMediaFullHasNoAcceptFallback(t *testing.T) {
	old := acceptFallbackDelay
	acceptFallbackDelay = 20 * time.Millisecond
	defer func() { acceptFallbackDelay = old }()

	eng, call, _ := testEngineWithIncomingCall()
	accepts := acceptCounter(eng)
	if err := eng.answer(call); err != nil {
		t.Fatalf("answer: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if accepts() != 0 {
		t.Fatalf("default mode sent %d accept(s) without a mute_v2", accepts())
	}
}

// A call that ended before the fallback fires gets no accept.
func TestEarlyMediaFallbackSkipsAnEndedCall(t *testing.T) {
	old := acceptFallbackDelay
	acceptFallbackDelay = 50 * time.Millisecond
	defer func() { acceptFallbackDelay = old }()

	eng, call, _ := testEngineWithIncomingCall()
	accepts := acceptCounter(eng)
	eng.calls["CID"].earlyMode = EarlyMediaHoldRTPAndRTCP
	if err := eng.answer(call); err != nil {
		t.Fatalf("answer: %v", err)
	}
	eng.finishCall("CID", "hangup")
	time.Sleep(150 * time.Millisecond)
	if accepts() != 0 {
		t.Fatalf("accept sent for an ended call: %d", accepts())
	}
}
