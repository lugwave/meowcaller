package meowcaller

import (
	"go.mau.fi/whatsmeow/types"

	"github.com/purpshell/meowcaller/diag"
	"github.com/rs/zerolog"
)

// Option configures optional, non-behavioral aspects of the call/media types —
// currently the diagnostic logger. The zero configuration logs nothing.
type Option func(*config)

type config struct {
	log  zerolog.Logger
	diag *diag.Recorder

	offerGate func(Offer) bool

	earlyMedia func(Offer) EarlyMediaMode
}

func resolveConfig(opts []Option) config {
	c := config{log: zerolog.Nop()}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// WithLogger sets the zerolog logger for debug/trace diagnostics. The library never
// configures logging itself; without this option the types are silent at zero cost.
// Pass the logger from a context, e.g. WithLogger(*zerolog.Ctx(ctx)).
func WithLogger(l zerolog.Logger) Option {
	return func(c *config) { c.log = l }
}

// WithDiagnostics attaches a developer-only *diag.Recorder that dumps exact,
// per-category call diagnostics (including raw secrets and media) to JSONL files.
// This is an opt-in maintainer carve-out from the library's sanitized logging and
// must never be enabled in production. Without it the recorder is nil and every
// diag emit is a no-op at zero cost.
func WithDiagnostics(rec *diag.Recorder) Option {
	return func(c *config) { c.diag = rec }
}

// Offer is what an offer gate is told about an inbound call before the library
// touches it.
type Offer struct {
	CallID string
	// From is the stanza's sender; Creator is the call-creator, the same user on a
	// 1:1 call.
	From    types.JID
	Creator types.JID
	// Group reports a group call or call-link invite.
	Group bool
	// Video reports that the offer advertises video.
	Video bool
}

// WithOfferGate makes the library ask before it engages with an inbound offer. When
// fn returns false nothing is decrypted, no <preaccept> is sent, no media starts and
// OnIncomingCall does not fire: the device stays as passive as a plain whatsmeow
// client. Without a gate every offer is engaged. fn runs on whatsmeow's event
// dispatch: keep it short.
func WithOfferGate(fn func(Offer) bool) Option {
	return func(c *config) { c.offerGate = fn }
}

// EarlyMediaMode says what an inbound 1:1 call sends to the relay while it is still
// ringing here, before Answer. EXPERIMENTAL (lugwave): the default is the upstream
// PR #43 behavior; the other modes exist to measure, with real calls, which of the
// early packets makes the caller's phone count the call as connected.
type EarlyMediaMode int

const (
	// EarlyMediaFull is the default (PR #43): from the offer the library connects
	// the relays, allocates, pings and sends audio RTP (silence) and SRTCP sender
	// reports. The caller's phone starts its call clock (audio_duration) on that
	// media, before Answer.
	EarlyMediaFull EarlyMediaMode = iota
	// EarlyMediaHoldRTP connects, allocates, pings and sends SRTCP sender reports
	// from the offer, but no audio RTP until Answer.
	EarlyMediaHoldRTP
	// EarlyMediaHoldRTPAndRTCP connects, allocates and pings from the offer (the
	// relay transport and the 1 Hz allocate+ping keepalive), but sends neither
	// audio RTP nor SRTCP until Answer. Inbound packets are still read and decoded.
	EarlyMediaHoldRTPAndRTCP
	// EarlyMediaOff starts no media before Answer: the behavior before PR #43
	// (expected: the call clock is right, but a late Answer gets no inbound audio).
	EarlyMediaOff
)

// String returns the mode's stable name, as logged.
func (m EarlyMediaMode) String() string {
	switch m {
	case EarlyMediaFull:
		return "full"
	case EarlyMediaHoldRTP:
		return "hold_rtp"
	case EarlyMediaHoldRTPAndRTCP:
		return "hold_rtp_rtcp"
	case EarlyMediaOff:
		return "off"
	}
	return "unknown"
}

// holdsOutbound reports whether audio RTP waits for Answer in this mode.
func (m EarlyMediaMode) holdsOutbound() bool {
	return m == EarlyMediaHoldRTP || m == EarlyMediaHoldRTPAndRTCP
}

// WithInboundEarlyMedia chooses, per inbound 1:1 offer, what is sent before Answer
// (see EarlyMediaMode). fn runs on whatsmeow's event dispatch, after the offer gate:
// keep it short. Without it every call uses EarlyMediaFull. Group calls and outbound
// calls are not affected.
func WithInboundEarlyMedia(fn func(Offer) EarlyMediaMode) Option {
	return func(c *config) { c.earlyMedia = fn }
}
