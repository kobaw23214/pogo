// Controlled local codec negotiation probe; stdout is private IPC to the Node harness.
package main

import (
	"encoding/json"
	"github.com/pion/webrtc/v4"
	"os"
)

func main() {
	var offers []map[string]any
	for _, profile := range []string{"42c033", "42e033", "42c034", "42e01f"} {
		m := &webrtc.MediaEngine{}
		cap := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "profile-level-id=" + profile + ";packetization-mode=1;level-asymmetry-allowed=1"}
		if m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: cap, PayloadType: 102}, webrtc.RTPCodecTypeVideo) != nil {
			os.Exit(1)
		}
		pc, e := webrtc.NewAPI(webrtc.WithMediaEngine(m)).NewPeerConnection(webrtc.Configuration{})
		if e != nil {
			os.Exit(1)
		}
		track, e := webrtc.NewTrackLocalStaticRTP(cap, "video", "pogo")
		if e != nil {
			os.Exit(1)
		}
		if _, e = pc.AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); e != nil {
			os.Exit(1)
		}
		offer, e := pc.CreateOffer(nil)
		if e != nil {
			os.Exit(1)
		}
		offers = append(offers, map[string]any{"profile": profile, "offer": offer})
		_ = pc.Close()
	}
	_ = json.NewEncoder(os.Stdout).Encode(offers)
}
