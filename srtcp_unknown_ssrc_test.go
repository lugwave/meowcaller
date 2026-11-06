package meowcaller

import (
	"bytes"
	"testing"

	"github.com/purpshell/meowcaller/rtp"
	"github.com/purpshell/meowcaller/srtp"
)

// An SRTCP packet from an SSRC outside the call's streams (ssrc 1, seen at
// accept on a real call) is told apart from one whose HMAC does not verify on a
// known SSRC: the receive loop logs the first at Debug and the second at Warn.
func TestParticipantReceiveRegistrySeparatesUnknownSSRCFromAuthFailure(t *testing.T) {
	callKey := iota32()
	self := mediaTestJID("111111111111111", 14)
	peer := mediaTestJID("222222222222222", 0)
	registry, err := newParticipantReceiveRegistry("CID", callKey, self.String(), peer.String(), nil)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	participantID := rtp.FormatE2ESrtpParticipantID(peer.String())
	peerSSRC, err := rtp.DeriveWasmParticipantSsrc("CID", participantID, 0)
	if err != nil {
		t.Fatalf("derive peer SSRC: %v", err)
	}
	keys, err := srtp.DeriveE2eSrtcpKeys(callKey, participantID)
	if err != nil {
		t.Fatalf("derive peer SRTCP keys: %v", err)
	}
	var stats rtp.RtcpSenderStats
	var cnameEntropy [12]byte
	cname := rtp.BuildWhatsappRtcpCname(cnameEntropy)
	plain := rtp.BuildSenderReportWithSdesAndReception(peerSSRC, &stats, 3, &cname, nil, false)
	protected, err := srtp.ProtectSrtcp(&keys, peerSSRC, 5, plain)
	if err != nil {
		t.Fatalf("protect peer SRTCP: %v", err)
	}

	got, index, result := registry.unprotectSRTCP(peerSSRC, protected)
	if result != srtcpUnprotected || index != 5 || !bytes.Equal(got, plain) {
		t.Fatalf("valid peer SRTCP = (%x, %d, %d), want authenticated index 5", got, index, result)
	}

	tampered := append([]byte(nil), protected...)
	tampered[len(tampered)-1] ^= 0xff
	if _, _, result = registry.unprotectSRTCP(peerSSRC, tampered); result != srtcpAuthFailed {
		t.Fatalf("tampered SRTCP on the peer SSRC = %d, want srtcpAuthFailed", result)
	}
	if _, _, ok := registry.UnprotectSRTCP(peerSSRC, tampered); ok {
		t.Fatal("UnprotectSRTCP accepted a tampered packet")
	}

	if _, _, result = registry.unprotectSRTCP(1, protected); result != srtcpUnknownSSRC {
		t.Fatalf("SRTCP from ssrc 1 = %d, want srtcpUnknownSSRC", result)
	}
	if _, _, ok := registry.UnprotectSRTCP(1, protected); ok {
		t.Fatal("UnprotectSRTCP accepted a packet from an unknown SSRC")
	}
}
