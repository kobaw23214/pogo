package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/interceptor/pkg/twcc"
	"github.com/pion/logging"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
)

const absCaptureURI = "http://www.webrtc.org/experiments/rtp-hdrext/abs-capture-time"
const playoutURI = "http://www.webrtc.org/experiments/rtp-hdrext/playout-delay"
const contentURI = "http://www.webrtc.org/experiments/rtp-hdrext/video-content-type"

type peer struct {
	scope                                                                        scope
	id                                                                           string
	owner                                                                        *client
	server                                                                       *server
	width, height                                                                uint16
	minContent                                                                   uint64
	codec, profile                                                               string
	pc                                                                           *webrtc.PeerConnection
	track                                                                        *webrtc.TrackLocalStaticRTP
	sender                                                                       *webrtc.RTPSender
	payloader                                                                    rtp.Payloader
	mu                                                                           sync.Mutex
	pending                                                                      *frame
	waitingIDR                                                                   bool
	notify                                                                       chan struct{}
	done                                                                         chan struct{}
	once                                                                         sync.Once
	signalMu                                                                     sync.Mutex
	remoteReady                                                                  bool
	candidates                                                                   []webrtc.ICECandidateInit
	extensions                                                                   map[string]uint8
	sequence                                                                     uint16
	timestampBase                                                                uint32
	connected                                                                    atomic.Bool
	lastCaptureNS, lastEncodeNS                                                  atomic.Uint64
	received, sent, dropped, packets, bytes, keyframes, plis, nacks, writeErrors atomic.Uint64
	targetBitrate                                                                atomic.Int64
}

func (s *server) createPeer(c *client, a peerArgs) (any, error) {
	s.mu.Lock()
	state := s.sessions[a.Session]
	if state == nil || state.owner != c || state.pages[a.Page] == nil || state.pages[a.Page].generation != a.Generation {
		s.mu.Unlock()
		return nil, errScope
	}
	key := viewKey{a.Session, a.Page, a.View}
	if s.creating[key] != nil {
		s.mu.Unlock()
		return nil, errScope
	}
	if len(s.peers)+len(s.creating) >= s.cfg.MaxPeers {
		s.mu.Unlock()
		return nil, errCapacity
	}
	for _, v := range s.peers {
		if v.scope.Session == a.Session && v.scope.Page == a.Page && v.scope.View == a.View {
			s.mu.Unlock()
			return nil, errScope
		}
	}
	s.creating[key] = c
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.creating, key); s.mu.Unlock() }()
	p := &peer{scope: a.scope, id: randomID(), owner: c, server: s, width: a.Width, height: a.Height, minContent: a.MinContent, codec: a.Codec, profile: strings.ToLower(a.Profile), waitingIDR: true, notify: make(chan struct{}, 1), done: make(chan struct{}), extensions: map[string]uint8{}}
	p.timestampBase = uint32(time.Now().UnixNano())
	p.sequence = uint16(time.Now().UnixNano())
	p.targetBitrate.Store(4_000_000)
	if s.cfg.Network == "lan" {
		p.targetBitrate.Store(12_000_000)
	}
	m := &webrtc.MediaEngine{}
	pt := webrtc.PayloadType(96)
	cap := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000, RTCPFeedback: []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "ccm", Parameter: "fir"}, {Type: "transport-cc"}}}
	if a.Codec == "h264" {
		cap.MimeType = webrtc.MimeTypeH264
		cap.SDPFmtpLine = "packetization-mode=1;level-asymmetry-allowed=1;profile-level-id=" + p.profile
		pt = 102
		p.payloader = &codecs.H264Payloader{}
	} else {
		p.payloader = &codecs.VP8Payloader{EnablePictureID: true}
	}
	if m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: cap, PayloadType: pt}, webrtc.RTPCodecTypeVideo) != nil {
		return nil, errMedia
	}
	if m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: "video/rtx", ClockRate: 90000, SDPFmtpLine: "apt=" + strconv.Itoa(int(pt))}, PayloadType: pt + 1}, webrtc.RTPCodecTypeVideo) != nil {
		return nil, errMedia
	}
	for _, uri := range []string{absCaptureURI, playoutURI, contentURI} {
		if m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: uri}, webrtc.RTPCodecTypeVideo) != nil {
			return nil, errMedia
		}
	}
	factory := logging.NewDefaultLoggerFactory()
	factory.Writer = io.Discard
	registry := &interceptor.Registry{}
	if webrtc.RegisterDefaultInterceptorsWithOptions(m, registry, webrtc.WithInterceptorLoggerFactory(factory), webrtc.WithNackResponderOptions(nack.ResponderSize(1024))) != nil {
		return nil, errMedia
	}
	headers, e := twcc.NewHeaderExtensionInterceptor()
	if e != nil {
		return nil, errMedia
	}
	if s.cfg.Network == "wan" {
		// No unbounded packet pacer: frame-aware synchronous pacing below owns
		// the sole pending frame. GCC still estimates bandwidth from TWCC/RTCP.
		ccFactory, e := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) {
			return gcc.NewSendSideBWE(gcc.SendSideBWEInitialBitrate(4_000_000), gcc.SendSideBWEMinBitrate(250_000), gcc.SendSideBWEMaxBitrate(12_000_000), gcc.SendSideBWEPacer(gcc.NewNoOpPacer()), gcc.WithLoggerFactory(factory))
		})
		if e != nil {
			return nil, errMedia
		}
		ccFactory.OnNewPeerConnection(func(_ string, bwe cc.BandwidthEstimator) {
			bwe.OnTargetBitrateChange(func(rate int) { p.targetBitrate.Store(int64(rate)); s.updateEncoderBudget(p.scope) })
		})
		registry.Add(ccFactory)
	}
	// Pion wraps local writers in registration order: the last added writer
	// runs first. TWCC must stamp the packet before GCC OnSent inspects it;
	// otherwise WAN fails its first RTP write with a missing-TWCC error.
	registry.Add(headers)
	settings := webrtc.SettingEngine{}
	settings.LoggerFactory = factory
	settings.SetLite(len(s.cfg.ICEServers) == 0)
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	settings.SetIncludeLoopbackCandidate(true)
	if settings.SetEphemeralUDPPortRange(s.cfg.PortMin, s.cfg.PortMax) != nil {
		return nil, errInvalid
	}
	if s.udpMux != nil {
		settings.SetICEUDPMux(s.udpMux)
	}
	if s.cfg.AdvertiseIP != "" {
		settings.SetNAT1To1IPs([]string{s.cfg.AdvertiseIP}, webrtc.ICECandidateTypeHost)
	}
	pc, e := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(registry), webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{ICEServers: s.cfg.ICEServers})
	if e != nil {
		return nil, errMedia
	}
	p.pc = pc
	ok := false
	defer func() {
		if !ok {
			p.close()
		}
	}()
	p.track, e = webrtc.NewTrackLocalStaticRTP(cap, "video", "pogo")
	if e != nil {
		return nil, errMedia
	}
	trans, e := pc.AddTransceiverFromTrack(p.track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	if e != nil {
		return nil, errMedia
	}
	p.sender = trans.Sender()
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		p.connected.Store(state == webrtc.PeerConnectionStateConnected)
		p.event(state.String())
		if state == webrtc.PeerConnectionStateConnected {
			s.requestIDR(p.scope, "connected")
		}
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			go s.removePeer(p.id)
		}
	})
	complete := webrtc.GatheringCompletePromise(pc)
	offer, e := pc.CreateOffer(nil)
	if e != nil || pc.SetLocalDescription(offer) != nil {
		return nil, errMedia
	}
	select {
	case <-complete:
	case <-time.After(5 * time.Second):
		return nil, errMedia
	case <-c.done:
		return nil, errMedia
	case <-s.done:
		return nil, errMedia
	}
	s.mu.Lock()
	state = s.sessions[a.Session]
	if state == nil || state.owner != c || state.pages[a.Page] == nil || state.pages[a.Page].generation != a.Generation || len(s.peers) >= s.cfg.MaxPeers {
		s.mu.Unlock()
		return nil, errScope
	}
	s.peers[p.id] = p
	s.mu.Unlock()
	ok = true
	go p.sendLoop()
	go p.readRTCP()
	s.updateEncoderBudget(p.scope)
	s.requestIDR(p.scope, "join")
	return map[string]any{"peer_id": p.id, "offer": pc.LocalDescription(), "settings": map[string]any{"width": a.Width, "height": a.Height, "frameRate": 60}}, nil
}
func (p *peer) event(state string) {
	p.owner.emit(map[string]any{"type": "event", "peer_id": p.id, "session": p.scope.Session, "page": p.scope.Page, "view": p.scope.View, "epoch": p.scope.Epoch, "generation": p.scope.Generation, "event": map[string]any{"type": "state", "state": state}})
}
func (p *peer) close() {
	p.once.Do(func() {
		close(p.done)
		p.mu.Lock()
		p.pending = nil
		p.waitingIDR = true
		p.mu.Unlock()
		if p.pc != nil {
			_ = p.pc.Close()
		}
	})
}
func (p *peer) answer(raw json.RawMessage) error {
	var answer webrtc.SessionDescription
	if len(raw) > maxJSON || strictJSON(raw, &answer) != nil || answer.Type != webrtc.SDPTypeAnswer {
		return errInvalid
	}
	p.signalMu.Lock()
	defer p.signalMu.Unlock()
	if p.remoteReady {
		return errScope
	}
	if p.codec == "h264" && !h264AnswerAllows(p.profile, answer.SDP) {
		return errMedia
	}
	if p.pc.SetRemoteDescription(answer) != nil {
		return errMedia
	}
	p.remoteReady = true
	for _, extension := range p.sender.GetParameters().HeaderExtensions {
		if extension.ID > 0 && extension.ID <= 255 {
			p.extensions[extension.URI] = uint8(extension.ID)
		}
	}
	for _, candidate := range p.candidates {
		if p.pc.AddICECandidate(candidate) != nil {
			return errMedia
		}
	}
	p.candidates = nil
	return nil
}
func (p *peer) candidate(raw json.RawMessage) error {
	var candidate webrtc.ICECandidateInit
	if len(raw) > 16384 || strictJSON(raw, &candidate) != nil || len(candidate.Candidate) > 8192 {
		return errInvalid
	}
	p.signalMu.Lock()
	defer p.signalMu.Unlock()
	if !p.remoteReady {
		if len(p.candidates) >= 64 {
			return errCapacity
		}
		p.candidates = append(p.candidates, candidate)
		return nil
	}
	if p.pc.AddICECandidate(candidate) != nil {
		return errMedia
	}
	return nil
}
func h264AnswerAllows(profile, sdp string) bool {
	source, e := hex.DecodeString(profile)
	if e != nil || len(source) != 3 {
		return false
	}
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "a=fmtp:102 ") {
			continue
		}
		values := map[string]string{}
		for _, field := range strings.Split(strings.TrimPrefix(line, "a=fmtp:102 "), ";") {
			pair := strings.SplitN(strings.TrimSpace(field), "=", 2)
			if len(pair) == 2 {
				values[pair[0]] = pair[1]
			}
		}
		peer, e := hex.DecodeString(values["profile-level-id"])
		if e != nil || len(peer) != 3 || values["packetization-mode"] != "1" {
			return false
		}
		// Constraint-set 2 differs between x264 42c0 and Chrome 42e0, but both
		// are constrained baseline. The receive level must not be lower.
		profileMatch := source[0] == peer[0] && (source[1]&0xc0) == (peer[1]&0xc0)
		return profileMatch && peer[2] >= source[2]
	}
	return false
}
func (p *peer) compatible(f *frame) bool {
	if f.Codec != p.codec || f.Width != p.width || f.Height != p.height || f.ContentSeq < p.minContent || f.Generation != p.scope.Generation {
		return false
	}
	if p.codec == "h264" {
		configured, _ := hex.DecodeString(p.profile)
		for _, nal := range annexB(f.Payload) {
			if nal[0]&31 == 7 {
				if len(nal) < 4 || nal[1] != configured[0] || (nal[2]&0xc0) != (configured[1]&0xc0) || nal[3] > configured[2] {
					return false
				}
			}
		}
	}
	return true
}
func (p *peer) enqueue(f *frame) {
	p.received.Add(1)
	if !p.connected.Load() {
		p.dropped.Add(1)
		return
	}
	if !p.compatible(f) {
		p.dropped.Add(1)
		p.mu.Lock()
		p.pending = nil
		p.waitingIDR = true
		p.mu.Unlock()
		p.server.requestIDR(p.scope, "frame-fence")
		return
	}
	p.lastCaptureNS.Store(f.CaptureNS)
	if f.EncodeEndNS >= f.EncodeStartNS {
		p.lastEncodeNS.Store(f.EncodeEndNS - f.EncodeStartNS)
	}
	p.mu.Lock()
	request := false
	if p.pending != nil {
		p.dropped.Add(1)
		p.pending = nil
		p.waitingIDR = true
		request = true
	}
	if p.waitingIDR && !f.Keyframe {
		p.dropped.Add(1)
		request = true
	} else {
		p.pending = f
		if f.Keyframe {
			p.waitingIDR = false
		}
	}
	p.mu.Unlock()
	if request {
		p.server.requestIDR(p.scope, "queue-drop")
	}
	select {
	case p.notify <- struct{}{}:
	default:
	}
}
func (p *peer) sendLoop() {
	for {
		select {
		case <-p.done:
			return
		case <-p.notify:
			p.mu.Lock()
			f := p.pending
			p.pending = nil
			p.mu.Unlock()
			if f == nil {
				continue
			}
			if p.writeFrame(f) != nil {
				p.writeErrors.Add(1)
				p.event("failed")
				p.server.removePeer(p.id)
				return
			}
		}
	}
}
func (p *peer) writeFrame(f *frame) error {
	payloads := p.payloader.Payload(1100, f.Payload)
	if len(payloads) == 0 {
		return errMedia
	}
	timestamp := p.timestampBase + uint32((f.CaptureNS/100000)*9+(f.CaptureNS%100000)*9/100000)
	p.signalMu.Lock()
	extensions := make(map[string]uint8, len(p.extensions))
	for k, v := range p.extensions {
		extensions[k] = v
	}
	p.signalMu.Unlock()
	for i, payload := range payloads {
		select {
		case <-p.done:
			return errMedia
		default:
		}
		packet := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: p.sequence, Timestamp: timestamp, Marker: i == len(payloads)-1}, Payload: payload}
		p.sequence++
		if i == 0 {
			if id := extensions[absCaptureURI]; id != 0 {
				capture := time.Now().Add(-time.Duration(monotonicNS() - int64(f.CaptureNS)))
				bytes, _ := rtp.NewAbsCaptureTimeExtension(capture).Marshal()
				if packet.SetExtension(id, bytes) != nil {
					return errMedia
				}
			}
			if id := extensions[contentURI]; id != 0 {
				if packet.SetExtension(id, []byte{1}) != nil {
					return errMedia
				}
			}
			if id := extensions[playoutURI]; id != 0 && p.server.cfg.PlayoutDelay == "zero" {
				bytes, _ := (&rtp.PlayoutDelayExtension{MinDelay: 0, MaxDelay: 0}).Marshal()
				if packet.SetExtension(id, bytes) != nil {
					return errMedia
				}
			}
		}
		if err := p.track.WriteRTP(packet); err != nil {
			// Retain the cause for private tests/callers. Public state events and
			// RPC errors still expose only their fixed sanitized failure code.
			return fmt.Errorf("%w: %w", errMedia, err)
		}
		p.packets.Add(1)
		p.bytes.Add(uint64(len(payload)))
		if p.server.cfg.Network == "wan" {
			// Synchronous packet pacing: no hidden second packet/frame queue.
			rate := p.targetBitrate.Load()
			if rate < 250_000 {
				rate = 250_000
			}
			pause := time.Duration(int64(len(payload)+64) * 8 * int64(time.Second) / rate)
			timer := time.NewTimer(pause)
			select {
			case <-timer.C:
			case <-p.done:
				timer.Stop()
				return errMedia
			}
		}
	}
	p.sent.Add(1)
	if f.Keyframe {
		p.keyframes.Add(1)
	}
	return nil
}
func (p *peer) readRTCP() {
	for {
		packets, _, err := p.sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, packet := range packets {
			switch packet.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				p.plis.Add(1)
				p.server.requestIDR(p.scope, "pli")
			case *rtcp.TransportLayerNack:
				p.nacks.Add(1)
				p.server.requestIDR(p.scope, "nack")
			}
		}
	}
}
func (p *peer) stats() map[string]any {
	p.mu.Lock()
	waiting := p.waitingIDR
	pending := p.pending != nil
	p.mu.Unlock()
	mime := webrtc.MimeTypeVP8
	if p.codec == "h264" {
		mime = webrtc.MimeTypeH264
	}
	return map[string]any{"codec": p.codec, "mime_type": mime, "width": p.width, "height": p.height, "capture_width": p.width, "capture_height": p.height, "capture_fps": nil, "capture_max_fps": 60, "connected": p.connected.Load(), "frames_received": p.received.Load(), "frames_sent": p.sent.Load(), "frames_dropped": p.dropped.Load(), "packets_sent": p.packets.Load(), "bytes_sent": p.bytes.Load(), "keyframes_sent": p.keyframes.Load(), "pli_count": p.plis.Load(), "nack_count": p.nacks.Load(), "write_errors": p.writeErrors.Load(), "waiting_keyframe": waiting, "pending_frames": boolInt(pending), "target_bitrate": p.targetBitrate.Load(), "last_capture_ns": p.lastCaptureNS.Load(), "last_encode_ms": float64(p.lastEncodeNS.Load()) / 1e6, "synchronous_pacing": p.server.cfg.Network == "wan"}
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
