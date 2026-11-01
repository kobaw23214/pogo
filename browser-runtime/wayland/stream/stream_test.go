package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

func testConfig(t *testing.T) config {
	t.Helper()
	dir, e := os.MkdirTemp("/tmp", "pogo-stream-test-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return config{Dir: dir, Token: strings.Repeat("x", 64), PortMin: 21980, PortMax: 22011, MaxPeers: 4, Network: "lan"}
}
func vp8Payload(w, h uint16, key bool) []byte {
	if !key {
		return []byte{1, 0, 0, 0xaa, 0xbb}
	}
	b := make([]byte, 4096)
	b[3] = 0x9d
	b[4] = 1
	b[5] = 0x2a
	binary.LittleEndian.PutUint16(b[6:8], w)
	binary.LittleEndian.PutUint16(b[8:10], h)
	return b
}
func testFrame(seq uint64, key bool) *frame {
	payload := vp8Payload(16, 16, key)
	return &frame{frameHeader: frameHeader{Codec: "vp8", Page: "t1", Display: 2, Generation: 1, Seq: seq, ContentSeq: 100, CaptureNS: uint64(monotonicNS()), Width: 16, Height: 16, Keyframe: key, PayloadBytes: uint32(len(payload))}, Payload: payload}
}
func wire(f *frame) []byte {
	b, _ := json.Marshal(f.frameHeader)
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	return append(append(n[:], b...), f.Payload...)
}
func fakeClient(s *server) *client {
	return &client{server: s, queue: make(chan any, 32), done: make(chan struct{})}
}
func rpcArgs(t *testing.T, s *server, c *client, op string, a any) (any, error) {
	t.Helper()
	b, _ := json.Marshal(a)
	return s.rpc(c, request{ID: "r1", Op: op, Args: b})
}
func registerTestScope(t *testing.T, s *server, c *client) scope {
	t.Helper()
	sc := scope{Session: "s1", Page: "t1", View: "v1", Epoch: 1, Generation: 1}
	if _, e := rpcArgs(t, s, c, "register_session", map[string]any{"session": sc.Session, "token": strings.Repeat("s", 64)}); e != nil {
		t.Fatal(e)
	}
	if _, e := rpcArgs(t, s, c, "register_page", map[string]any{"session": sc.Session, "page": sc.Page, "display_id": 2, "generation": 1}); e != nil {
		t.Fatal(e)
	}
	return sc
}

func TestFrameWirePartialReadsAndTruncation(t *testing.T) {
	raw := wire(testFrame(1, true))
	f, e := readFrame(&shortReader{data: raw})
	if e != nil || !bytes.Equal(f.Payload, testFrame(1, true).Payload) {
		t.Fatal("bounded full frame reconstruction failed", e)
	}
	if _, e = readFrame(bytes.NewReader(raw[:len(raw)-1])); !errors.Is(e, io.ErrUnexpectedEOF) {
		t.Fatal("truncation was accepted")
	}
}

type shortReader struct{ data []byte }

func (r *shortReader) Read(b []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := len(b)
	if n > 3 {
		n = 3
	}
	if n > len(r.data) {
		n = len(r.data)
	}
	copy(b, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}
func TestFrameWireRejectsOversizeUnknownCodecAndFalseKeyframe(t *testing.T) {
	for _, mutate := range []func(*frame){func(f *frame) { f.PayloadBytes = maxPayload + 1 }, func(f *frame) { f.Codec = "jpeg" }, func(f *frame) { f.Keyframe = false }, func(f *frame) { f.Width = 18 }, func(f *frame) { f.Generation = 0 }, func(f *frame) { f.Width = 17 }, func(f *frame) { f.EncodeStartNS = 20; f.EncodeEndNS = 10 }, func(f *frame) { f.EncodeEndNS = 10 }} {
		f := testFrame(1, true)
		mutate(f)
		if _, e := readFrame(bytes.NewReader(wire(f))); e == nil {
			t.Fatal("invalid producer frame accepted")
		}
	}
}
func TestCreateCapacityIncludesPeersStillGathering(t *testing.T) {
	s, _ := newServer(testConfig(t))
	c := fakeClient(s)
	sc := registerTestScope(t, s, c)
	a := peerArgs{scope: sc, Codec: "vp8", Width: 16, Height: 16}
	s.creating[viewKey{sc.Session, sc.Page, sc.View}] = c
	if _, e := s.createPeer(c, a); !errors.Is(e, errScope) {
		t.Fatal("duplicate view allocated while gathering")
	}
	a.View = "new-view"
	for _, v := range []string{"v2", "v3", "v4"} {
		s.creating[viewKey{sc.Session, sc.Page, v}] = c
	}
	if _, e := s.createPeer(c, a); !errors.Is(e, errCapacity) {
		t.Fatal("pending ICE allocations bypassed peer bound")
	}
}
func TestStrictJSONRejectsTrailingAndUnknownFields(t *testing.T) {
	for _, raw := range []string{`{"session":"s","unexpected":1}`, `{"session":"s"}{}`} {
		var target struct {
			Session string `json:"session"`
		}
		if strictJSON([]byte(raw), &target) == nil {
			t.Fatal("ambiguous control message accepted")
		}
	}
}
func TestQueueDropRequiresNewKeyframe(t *testing.T) {
	s, _ := newServer(testConfig(t))
	c := fakeClient(s)
	sc := registerTestScope(t, s, c)
	p := &peer{scope: sc, server: s, width: 16, height: 16, minContent: 100, codec: "vp8", waitingIDR: true, notify: make(chan struct{}, 1), done: make(chan struct{})}
	p.connected.Store(true)
	p.enqueue(testFrame(1, false))
	if p.pending != nil {
		t.Fatal("delta released before keyframe")
	}
	p.enqueue(testFrame(2, true))
	if p.pending == nil || p.waitingIDR {
		t.Fatal("keyframe did not unlock")
	}
	p.enqueue(testFrame(3, false))
	if p.pending != nil || !p.waitingIDR {
		t.Fatal("drop left a broken prediction chain writable")
	}
	p.enqueue(testFrame(4, false))
	if p.pending != nil {
		t.Fatal("delta following drop was released")
	}
	p.enqueue(testFrame(5, true))
	if p.pending == nil || p.pending.Seq != 5 || p.waitingIDR {
		t.Fatal("latest keyframe did not recover")
	}
}
func TestCausalAndGenerationFenceDropsBeforeQueue(t *testing.T) {
	s, _ := newServer(testConfig(t))
	p := &peer{scope: scope{Generation: 2}, server: s, width: 16, height: 16, minContent: 200, codec: "vp8", waitingIDR: true, notify: make(chan struct{}, 1), done: make(chan struct{})}
	p.connected.Store(true)
	f := testFrame(1, true)
	p.enqueue(f)
	if p.pending != nil {
		t.Fatal("wrong generation released")
	}
	f.Generation = 2
	p.enqueue(f)
	if p.pending != nil {
		t.Fatal("pre-clean-marker content released")
	}
	f.ContentSeq = 200
	p.enqueue(f)
	if p.pending == nil {
		t.Fatal("causal clean keyframe not released")
	}
}

func TestHTTPCompatibleSessionIdentifierBounds(t *testing.T) {
	for _, n := range []int{129, 160} {
		sc := scope{Session: strings.Repeat("s", n), Page: "t1", View: "v1", Epoch: 1, Generation: 1}
		if !sc.valid() {
			t.Fatal("valid worker session ID rejected", n)
		}
	}
	sc := scope{Session: strings.Repeat("s", 161), Page: "t1", View: "v1", Epoch: 1, Generation: 1}
	if sc.valid() {
		t.Fatal("unbounded session ID accepted")
	}
}
func TestSamePageAcrossSessionsDoesNotAlias(t *testing.T) {
	s, _ := newServer(testConfig(t))
	c := fakeClient(s)
	sc := registerTestScope(t, s, c)
	if _, e := rpcArgs(t, s, c, "register_session", map[string]any{"session": "s2", "token": strings.Repeat("q", 64)}); e != nil {
		t.Fatal(e)
	}
	if _, e := rpcArgs(t, s, c, "register_page", map[string]any{"session": "s2", "page": "t1", "display_id": 2, "generation": 1}); e != nil {
		t.Fatal(e)
	}
	if s.sessions[sc.Session].pages[sc.Page] == s.sessions["s2"].pages["t1"] {
		t.Fatal("session page identity aliased")
	}
	other := fakeClient(s)
	if _, e := rpcArgs(t, s, other, "unregister_page", map[string]any{"session": "s1", "page": "t1"}); !errors.Is(e, errScope) {
		t.Fatal("foreign control connection could unregister a page")
	}
}
func TestGenerationNeverRollsBackOrRebindsSameGeneration(t *testing.T) {
	s, _ := newServer(testConfig(t))
	c := fakeClient(s)
	registerTestScope(t, s, c)
	for _, args := range []map[string]any{{"session": "s1", "page": "t1", "display_id": 3, "generation": 1}, {"session": "s1", "page": "t2", "display_id": 2, "generation": 1}} {
		if _, e := rpcArgs(t, s, c, "register_page", args); !errors.Is(e, errScope) {
			t.Fatal("ambiguous display owner accepted")
		}
	}
}
func TestH264RejectsBrowser31ForSource51(t *testing.T) {
	if h264AnswerAllows("42c033", "a=fmtp:102 packetization-mode=1;profile-level-id=42e01f;level-asymmetry-allowed=1\r\n") {
		t.Fatal("level-asymmetry bypassed receiver capability")
	}
	if !h264AnswerAllows("42c033", "a=fmtp:102 packetization-mode=1;profile-level-id=42e033\r\n") {
		t.Fatal("actual compatible constrained baseline rejected")
	}
}

func TestDeploymentICEParsingAndIPv6ExplicitRejection(t *testing.T) {
	servers, e := parseICE(`[{"urls":"stun:example.invalid:3478"},{"urls":["turn:example.invalid:3478?transport=tcp"],"username":"private-user","credential":"private-secret"}]`)
	if e != nil || len(servers) != 2 || servers[1].Username != "private-user" {
		t.Fatal("trusted deployment ICE configuration lost")
	}
	for _, raw := range []string{`null`, `{}`, `[{"urls":"https://example.invalid"}]`, `[{"urls":"stun:example.invalid","token":"x"}]`, `[{"urls":[]}]`, `[{"urls":"turn:example.invalid","credential":4}]`} {
		if _, e = parseICE(raw); e == nil {
			t.Fatal("invalid or unknown ICE field accepted")
		}
	}
	c := testConfig(t)
	c.AdvertiseIP = "::1"
	if !errors.Is(c.validate(), errIPv6) {
		t.Fatal("IPv6 was silently treated as IPv4")
	}
	c.AdvertiseIP = "127.0.0.1"
	c.PlayoutDelay = "zero"
	if c.validate() != nil {
		t.Fatal("independent playout option rejected")
	}
}

func TestSharedPageEncoderUsesMinimumBudget(t *testing.T) {
	s, _ := newServer(testConfig(t))
	s.cfg.Network = "wan"
	c := fakeClient(s)
	sc := registerTestScope(t, s, c)
	h := &frameHost{queue: make(chan any, 16), done: make(chan struct{})}
	s.sessions[sc.Session].host = h
	p1 := &peer{scope: sc}
	p1.targetBitrate.Store(4_000_000)
	p2 := &peer{scope: sc}
	p2.targetBitrate.Store(1_000_000)
	s.peers["1"] = p1
	s.peers["2"] = p2
	s.updateEncoderBudget(sc)
	select {
	case raw := <-h.queue:
		if raw.(map[string]any)["bitrate_kbps"] != int64(1000) {
			t.Fatal("shared source ignored slow peer")
		}
	default:
		t.Fatal("encoder budget was not sent")
	}
	s.updateEncoderBudget(sc)
	if len(h.queue) != 0 {
		t.Fatal("duplicate unchanged budgets were queued")
	}
}
func TestPendingCandidatesBoundedBeforeAnswer(t *testing.T) {
	p := &peer{}
	raw := json.RawMessage(`{"candidate":""}`)
	for i := 0; i < 64; i++ {
		if p.candidate(raw) != nil {
			t.Fatal("bounded candidate rejected early")
		}
	}
	if !errors.Is(p.candidate(raw), errCapacity) {
		t.Fatal("candidate memory unbounded")
	}
}
func TestUnixAuthenticationRejectsWrongToken(t *testing.T) {
	s, e := newServer(testConfig(t))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Listen(); e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	conn, e := net.Dial("unix", filepath.Join(s.cfg.Dir, "ctl.sock"))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	_ = json.NewEncoder(conn).Encode(hello{Type: "hello", Version: 1, Token: strings.Repeat("bad", 22)})
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, e = bufio.NewReader(conn).ReadByte(); e == nil {
		t.Fatal("untrusted hello authenticated")
	}
}
func TestPeerOpsRequireCompleteScope(t *testing.T) {
	s, _ := newServer(testConfig(t))
	c := fakeClient(s)
	sc := registerTestScope(t, s, c)
	p := &peer{scope: sc, id: "peer1", owner: c, server: s, done: make(chan struct{})}
	s.peers[p.id] = p
	for _, change := range []func(*peerArgs){func(a *peerArgs) { a.Session = "s2" }, func(a *peerArgs) { a.Page = "t2" }, func(a *peerArgs) { a.View = "v2" }, func(a *peerArgs) { a.Epoch = 2 }, func(a *peerArgs) { a.Generation = 2 }} {
		a := peerArgs{scope: sc, PeerID: p.id}
		change(&a)
		if _, e := rpcArgs(t, s, c, "stats", a); !errors.Is(e, errScope) {
			t.Fatal("peer scope mismatch accepted")
		}
	}
}
func TestRealPionLoopbackRTPAndPLI(t *testing.T) {
	runRealPionLoopbackRTPAndPLI(t, "lan")
}
func TestRealPionWANLoopbackRTPAndPLI(t *testing.T) {
	runRealPionLoopbackRTPAndPLI(t, "wan")
}
func runRealPionLoopbackRTPAndPLI(t *testing.T, network string) {
	cfg := testConfig(t)
	cfg.Network = network
	s, e := newServer(cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Listen(); e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	c := fakeClient(s)
	sc := registerTestScope(t, s, c)
	result, e := s.createPeer(c, peerArgs{scope: sc, Codec: "vp8", Width: 16, Height: 16, MinContent: 100})
	if e != nil {
		t.Fatal(e)
	}
	id := result.(map[string]any)["peer_id"].(string)
	p := s.peers[id]
	defer p.close()
	receiver, e := webrtc.NewPeerConnection(webrtc.Configuration{})
	if e != nil {
		t.Fatal(e)
	}
	defer receiver.Close()
	got := make(chan *webrtc.TrackRemote, 1)
	receiver.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { got <- track })
	if receiver.SetRemoteDescription(*result.(map[string]any)["offer"].(*webrtc.SessionDescription)) != nil {
		t.Fatal("receiver rejected VP8 offer")
	}
	answer, e := receiver.CreateAnswer(nil)
	if e != nil {
		t.Fatal(e)
	}
	gather := webrtc.GatheringCompletePromise(receiver)
	if receiver.SetLocalDescription(answer) != nil {
		t.Fatal("answer failed")
	}
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
		t.Fatal("receiver gathering timed out")
	}
	raw, _ := json.Marshal(receiver.LocalDescription())
	if e = p.answer(raw); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !p.connected.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !p.connected.Load() {
		t.Fatal("authenticated DTLS peer did not connect")
	}
	// Exercise the real interceptor/DTLS sender and expose the actual first
	// WriteRTP failure, rather than hiding it behind an OnTrack timeout.
	if e = p.writeFrame(testFrame(1, true)); e != nil {
		t.Fatalf("%s first authenticated VP8 frame failed: %v", network, e)
	}
	var track *webrtc.TrackRemote
	select {
	case track = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("VP8 RTP did not reach real Pion receiver")
	}
	packet, _, e := track.ReadRTP()
	if e != nil || packet.PayloadType != 96 {
		t.Fatal("VP8 RTP payload negotiation failed", e)
	}
	if e = receiver.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(track.SSRC())}}); e != nil {
		t.Fatal(e)
	}
	deadline = time.Now().Add(2 * time.Second)
	for p.plis.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if p.plis.Load() == 0 {
		t.Fatal("receiver PLI did not reach sender feedback loop")
	}
	if e = receiver.WriteRTCP([]rtcp.Packet{&rtcp.TransportLayerNack{MediaSSRC: uint32(track.SSRC()), Nacks: []rtcp.NackPair{{PacketID: packet.SequenceNumber}}}}); e != nil {
		t.Fatal(e)
	}
	deadline = time.Now().Add(2 * time.Second)
	for p.nacks.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if p.nacks.Load() == 0 {
		t.Fatal("receiver NACK did not reach sender feedback loop")
	}
}
