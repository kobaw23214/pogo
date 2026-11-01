package web

// The live acceptance source deliberately uses a tiny arbitrary TCP protocol.
// Nothing here is a Pogo source adapter: the real model must implement its own
// client and decide how to turn these observations into sentinel events.
import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type sentinelLiveObservation struct {
	ID       string `json:"id"`
	Sequence int64  `json:"sequence"`
	Severity string `json:"severity"`
	Status   string `json:"status"`
	Acked    bool   `json:"acked"`
	Text     string `json:"text"`
}

type sentinelLiveSourceCommand struct {
	Command string `json:"command"`
	AfterID string `json:"after_id,omitempty"`
	At      int64  `json:"at"`
}

type sentinelLiveSourceStats struct {
	Connections       int                         `json:"connections"`
	ActiveConnections int                         `json:"active_connections"`
	ActiveFollowers   int                         `json:"active_followers"`
	StatusRequests    int                         `json:"status_requests"`
	FollowRequests    int                         `json:"follow_requests"`
	AuthenticationErr int                         `json:"authentication_errors"`
	ProtocolErrors    int                         `json:"protocol_errors"`
	MalformedReplies  int                         `json:"malformed_replies"`
	DeliveredLines    int                         `json:"delivered_lines"`
	SlowDisconnects   int                         `json:"slow_disconnects"`
	ReplayRequests    int                         `json:"replay_requests"`
	PublishedIDs      []string                    `json:"published_ids"`
	Commands          []sentinelLiveSourceCommand `json:"commands"`
}

type sentinelLiveSource struct {
	listener net.Listener
	address  string
	token    string
	mu       sync.Mutex
	closed   bool
	once     sync.Once
	wg       sync.WaitGroup
	sequence int64
	journal  []sentinelLiveObservation
	conns    map[net.Conn]bool
	watchers map[net.Conn]chan sentinelLiveObservation
	breaks   map[string]int
	stats    sentinelLiveSourceStats
}

func startSentinelLiveSource(t *testing.T) *sentinelLiveSource {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	s := &sentinelLiveSource{
		listener: listener,
		address:  net.JoinHostPort("127.0.0.1", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)),
		token:    "fixture-" + hex.EncodeToString(token[:]),
		conns:    map[net.Conn]bool{},
		watchers: map[net.Conn]chan sentinelLiveObservation{},
		breaks:   map[string]int{},
	}
	s.wg.Add(1)
	go s.accept()
	t.Cleanup(s.Close)
	return s
}

func (s *sentinelLiveSource) Address() string { return s.address }
func (s *sentinelLiveSource) Token() string   { return s.token }

// Publish creates one immutable observation. Republishing identical content
// with its prior ID simulates an upstream redelivery without moving the cursor.
func (s *sentinelLiveSource) Publish(observation sentinelLiveObservation) sentinelLiveObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		panic("publish to closed live acceptance source")
	}
	if observation.ID != "" {
		for _, prior := range s.journal {
			if prior.ID == observation.ID {
				observation.Sequence = prior.Sequence
				if !reflect.DeepEqual(prior, observation) {
					panic("live acceptance observation ID reused with different contents")
				}
				s.stats.ReplayRequests++
				s.deliverLocked(prior)
				return prior
			}
		}
	}
	if len(s.journal) >= 4096 {
		panic("live acceptance source journal exceeded its test budget")
	}
	s.sequence++
	observation.Sequence = s.sequence
	if observation.ID == "" {
		observation.ID = strconv.FormatInt(s.sequence, 10)
	}
	if strings.ContainsAny(observation.ID, " \t\r\n\x00") || len(observation.ID) > 128 {
		panic("invalid live acceptance source observation ID")
	}
	s.journal = append(s.journal, observation)
	s.stats.PublishedIDs = append(s.stats.PublishedIDs, observation.ID)
	s.deliverLocked(observation)
	return observation
}

func (s *sentinelLiveSource) deliverLocked(observation sentinelLiveObservation) {
	for conn, out := range s.watchers {
		select {
		case out <- observation:
		default:
			// A slow reader must reconnect and replay the authoritative journal;
			// silently dropping an accepted source record would invalidate evals.
			s.stats.SlowDisconnects++
			delete(s.watchers, conn)
			_ = conn.Close()
		}
	}
}

func (s *sentinelLiveSource) Snapshot() sentinelLiveObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.journal) == 0 {
		return sentinelLiveObservation{}
	}
	return s.journal[len(s.journal)-1]
}

func (s *sentinelLiveSource) Stats() sentinelLiveSourceStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.stats
	out.ActiveConnections = len(s.conns)
	out.ActiveFollowers = len(s.watchers)
	out.PublishedIDs = append([]string{}, s.stats.PublishedIDs...)
	out.Commands = append([]sentinelLiveSourceCommand{}, s.stats.Commands...)
	return out
}

// BreakNext is opt-in fault injection into the fixture, never a fabricated Pogo
// error. The next matching authenticated command receives a non-JSON line and
// a closed connection; the source immediately returns to normal afterward.
func (s *sentinelLiveSource) BreakNext(command string) {
	command = strings.ToUpper(strings.TrimSpace(command))
	if command != "STATUS" && command != "FOLLOW" {
		panic("BreakNext requires STATUS or FOLLOW")
	}
	s.mu.Lock()
	s.breaks[command]++
	s.mu.Unlock()
}

// DropFollowers preserves the address and journal but interrupts current TCP
// streams. A client can reconnect using its last committed source ID.
func (s *sentinelLiveSource) DropFollowers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn := range s.watchers {
		delete(s.watchers, conn)
		_ = conn.Close()
	}
}

func (s *sentinelLiveSource) Close() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		_ = s.listener.Close()
		for conn := range s.conns {
			_ = conn.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
}

func (s *sentinelLiveSource) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.conns[conn] = true
		s.stats.Connections++
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(conn)
	}
}

func (s *sentinelLiveSource) write(conn net.Conn, value any) error {
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if err := json.NewEncoder(conn).Encode(value); err != nil {
		return err
	}
	s.mu.Lock()
	s.stats.DeliveredLines++
	s.mu.Unlock()
	return nil
}

func (s *sentinelLiveSource) serve(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		conn.Close()
		s.mu.Lock()
		delete(s.conns, conn)
		delete(s.watchers, conn)
		s.mu.Unlock()
	}()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	scan := bufio.NewScanner(conn)
	scan.Buffer(make([]byte, 256), 4096)
	if !scan.Scan() {
		return
	}
	parts := strings.Fields(scan.Text())
	if len(parts) < 2 || subtle.ConstantTimeCompare([]byte(parts[1]), []byte(s.token)) != 1 {
		s.mu.Lock()
		s.stats.AuthenticationErr++
		s.mu.Unlock()
		_ = s.write(conn, map[string]string{"error": "unauthorized"})
		return
	}
	command := strings.ToUpper(parts[0])
	if command != "STATUS" && command != "FOLLOW" || command == "STATUS" && len(parts) != 2 || command == "FOLLOW" && len(parts) > 3 {
		s.mu.Lock()
		s.stats.ProtocolErrors++
		s.mu.Unlock()
		_ = s.write(conn, map[string]string{"error": "unknown command"})
		return
	}
	after := ""
	if len(parts) == 3 {
		after = parts[2]
	}
	s.mu.Lock()
	if len(s.stats.Commands) < 8192 {
		s.stats.Commands = append(s.stats.Commands, sentinelLiveSourceCommand{Command: command, AfterID: after, At: time.Now().UnixMilli()})
	}
	if command == "STATUS" {
		s.stats.StatusRequests++
	} else {
		s.stats.FollowRequests++
	}
	if s.breaks[command] > 0 {
		s.breaks[command]--
		s.stats.MalformedReplies++
		s.mu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		_, _ = fmt.Fprintln(conn, "TEMPORARY_SOURCE_ERROR retryable=1")
		return
	}
	if command == "STATUS" {
		if len(s.journal) == 0 {
			s.mu.Unlock()
			_ = s.write(conn, map[string]string{"error": "no snapshot"})
			return
		}
		observation := s.journal[len(s.journal)-1]
		s.mu.Unlock()
		_ = s.write(conn, observation)
		return
	}
	start := 0
	if after != "" {
		found := false
		for index, record := range s.journal {
			if record.ID == after {
				start, found = index+1, true
				break
			}
		}
		if !found {
			s.mu.Unlock()
			_ = s.write(conn, map[string]string{"error": "unknown cursor"})
			return
		}
	}
	replay := append([]sentinelLiveObservation{}, s.journal[start:]...)
	out := make(chan sentinelLiveObservation, 256)
	s.watchers[conn] = out
	s.mu.Unlock()
	for _, record := range replay {
		if err := s.write(conn, record); err != nil {
			return
		}
	}
	// Detect peer closure while a quiet source is blocked waiting for updates.
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		_ = conn.SetReadDeadline(time.Time{})
		var b [1]byte
		_, _ = conn.Read(b[:])
	}()
	for {
		select {
		case <-peerDone:
			return
		case record := <-out:
			if err := s.write(conn, record); err != nil {
				return
			}
		}
	}
}

func sentinelFixtureConnect(t *testing.T, source *sentinelLiveSource, command, token string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", source.Address(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := fmt.Fprintf(conn, "%s %s\n", command, token); err != nil {
		t.Fatal(err)
	}
	return conn, bufio.NewReader(conn)
}

func sentinelFixtureRead(t *testing.T, reader *bufio.Reader) sentinelLiveObservation {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var observation sentinelLiveObservation
	if err := json.Unmarshal(line, &observation); err != nil {
		t.Fatalf("invalid observation: %q: %v", line, err)
	}
	return observation
}

func TestSentinelLiveFixtureStatusStreamAndReplay(t *testing.T) {
	source := startSentinelLiveSource(t)
	first := source.Publish(sentinelLiveObservation{ID: "1", Severity: "low", Status: "open", Text: "baseline"})
	conn, reader := sentinelFixtureConnect(t, source, "STATUS", source.Token())
	if got := sentinelFixtureRead(t, reader); got != first {
		t.Fatalf("snapshot=%+v want=%+v", got, first)
	}
	conn.Close()
	stream, streamReader := sentinelFixtureConnect(t, source, "FOLLOW", source.Token())
	if got := sentinelFixtureRead(t, streamReader); got != first {
		t.Fatal("initial replay missing", got)
	}
	second := source.Publish(sentinelLiveObservation{ID: "2", Severity: "high", Status: "open", Text: "investigate this"})
	if got := sentinelFixtureRead(t, streamReader); got != second {
		t.Fatal("live update missing", got)
	}
	source.Publish(second)
	if got := sentinelFixtureRead(t, streamReader); got != second {
		t.Fatal("upstream redelivery changed record", got)
	}
	source.DropFollowers()
	if _, err := streamReader.ReadBytes('\n'); err == nil {
		t.Fatal("stream disconnect was not observable")
	}
	stream.Close()
	reconnected, err := net.DialTimeout("tcp", source.Address(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close()
	_ = reconnected.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(reconnected, "FOLLOW %s 1\n", source.Token())
	if got := sentinelFixtureRead(t, bufio.NewReader(reconnected)); got != second {
		t.Fatal("resume replay missing", got)
	}
	stats := source.Stats()
	if stats.StatusRequests != 1 || stats.FollowRequests != 2 || stats.ReplayRequests != 1 || !reflect.DeepEqual(stats.PublishedIDs, []string{"1", "2"}) {
		t.Fatalf("stats=%+v", stats)
	}
	for _, command := range stats.Commands {
		if strings.Contains(command.Command, source.Token()) {
			t.Fatal("command trace included credential")
		}
	}
}

func TestSentinelLiveFixtureFaultsAndAuthentication(t *testing.T) {
	source := startSentinelLiveSource(t)
	source.Publish(sentinelLiveObservation{ID: "1", Severity: "low", Status: "open", Acked: true})
	bad, reader := sentinelFixtureConnect(t, source, "STATUS", "wrong-token")
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "unauthorized") {
		t.Fatalf("authentication=%q %v", line, err)
	}
	bad.Close()
	source.BreakNext("STATUS")
	faulted, reader := sentinelFixtureConnect(t, source, "STATUS", source.Token())
	line, err = reader.ReadString('\n')
	if err != nil || json.Valid([]byte(line)) || !strings.Contains(line, "TEMPORARY_SOURCE_ERROR") {
		t.Fatalf("fixture fault=%q %v", line, err)
	}
	faulted.Close()
	normal, reader := sentinelFixtureConnect(t, source, "STATUS", source.Token())
	if got := sentinelFixtureRead(t, reader); got.ID != "1" || !got.Acked {
		t.Fatal("source did not recover after one injected fault", got)
	}
	normal.Close()
	unknown, err := net.DialTimeout("tcp", source.Address(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unknown.Close()
	unknown.SetDeadline(time.Now().Add(time.Second))
	fmt.Fprintf(unknown, "FOLLOW %s missing\n", source.Token())
	line, err = bufio.NewReader(unknown).ReadString('\n')
	if err != nil || !strings.Contains(line, "unknown cursor") {
		t.Fatalf("unknown cursor=%q %v", line, err)
	}
	stats := source.Stats()
	if stats.AuthenticationErr != 1 || stats.MalformedReplies != 1 || stats.StatusRequests != 2 || stats.FollowRequests != 1 {
		t.Fatalf("fault stats=%+v", stats)
	}
	source.Close()
	if _, err := net.DialTimeout("tcp", source.Address(), 100*time.Millisecond); err == nil || errors.Is(err, net.ErrClosed) {
		// Dial after listener shutdown should fail; a source-side net.ErrClosed
		// would indicate that this assertion accidentally reused a local handle.
		t.Fatalf("closed source dial=%v", err)
	}
}
