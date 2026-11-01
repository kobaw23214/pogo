package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/pion/webrtc/v4"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

type config struct {
	Dir              string
	Token            string
	AdvertiseIP      string
	PortMin, PortMax uint16
	MaxPeers         int
	Network          string
	PlayoutDelay     string
	ICEServers       []webrtc.ICEServer
}

var errIPv6 = errors.New("IPV6_UNSUPPORTED")
var iceURL = regexp.MustCompile(`^(stun|stuns|turn|turns):[^\s]+$`)

func parseICE(raw string) ([]webrtc.ICEServer, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 160*1024 {
		return nil, errInvalid
	}
	var rows []struct {
		URLs       json.RawMessage `json:"urls"`
		Username   string          `json:"username,omitempty"`
		Credential string          `json:"credential,omitempty"`
	}
	if strictJSON([]byte(raw), &rows) != nil || bytes.Equal(bytes.TrimSpace([]byte(raw)), []byte("null")) || len(rows) > 8 {
		return nil, errInvalid
	}
	var servers []webrtc.ICEServer
	for _, row := range rows {
		var urls []string
		var single string
		if json.Unmarshal(row.URLs, &single) == nil {
			urls = []string{single}
		} else if json.Unmarshal(row.URLs, &urls) != nil {
			return nil, errInvalid
		}
		if len(urls) < 1 || len(urls) > 8 || len(row.Username) > 1024 || len(row.Credential) > 1024 {
			return nil, errInvalid
		}
		for _, url := range urls {
			if len(url) > 2048 || !iceURL.MatchString(url) {
				return nil, errInvalid
			}
		}
		servers = append(servers, webrtc.ICEServer{URLs: urls, Username: row.Username, Credential: row.Credential, CredentialType: webrtc.ICECredentialTypePassword})
	}
	return servers, nil
}

func (c config) validate() error {
	if !filepath.IsAbs(c.Dir) || len(c.Token) < 32 || len(c.Token) > 256 || c.PortMin < 1024 || c.PortMax < c.PortMin || int(c.PortMax)-int(c.PortMin) > 1023 || c.MaxPeers < 1 || c.MaxPeers > 32 || (c.Network != "lan" && c.Network != "wan") {
		return errInvalid
	}
	if c.AdvertiseIP != "" && (net.ParseIP(c.AdvertiseIP) == nil || net.ParseIP(c.AdvertiseIP).To4() == nil) {
		if net.ParseIP(c.AdvertiseIP) != nil {
			return errIPv6
		}
		return errInvalid
	}
	if c.PlayoutDelay != "" && c.PlayoutDelay != "zero" && c.PlayoutDelay != "default" {
		return errInvalid
	}
	return nil
}
func run() error {
	dir := flag.String("dir", "/tmp/pogo-wl/wlstream", "private Unix socket directory")
	ports := flag.String("udp-range", "19780-19811", "deployment-selected UDP range")
	ip := flag.String("advertise-ip", "", "deployment-selected IPv4 address")
	network := flag.String("network", "wan", "lan or wan")
	playout := flag.String("playout-delay", "default", "zero or default")
	max := flag.Int("max-peers", 4, "maximum active video peers")
	flag.Parse()
	parts := strings.Split(*ports, "-")
	if len(parts) != 2 {
		return errInvalid
	}
	lo, e1 := strconv.ParseUint(parts[0], 10, 16)
	hi, e2 := strconv.ParseUint(parts[1], 10, 16)
	if e1 != nil || e2 != nil {
		return errInvalid
	}
	iceServers, err := parseICE(os.Getenv("POGO_WLSTREAM_ICE_SERVERS"))
	if err != nil {
		return err
	}
	c := config{Dir: *dir, Token: os.Getenv("POGO_WLSTREAM_TOKEN"), AdvertiseIP: *ip, PortMin: uint16(lo), PortMax: uint16(hi), MaxPeers: *max, Network: *network, PlayoutDelay: *playout, ICEServers: iceServers}
	if err := c.validate(); err != nil {
		return err
	}
	s, err := newServer(c)
	if err != nil {
		return err
	}
	defer s.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err = s.Listen(); err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]any{"ready": true, "version": 1, "transport": "webrtc", "udp": true})
	fmt.Println(string(b))
	<-ctx.Done()
	return nil
}
func main() {
	if err := run(); err != nil {
		code := "STREAM_FAILED"
		if errors.Is(err, errIPv6) {
			code = "IPV6_UNSUPPORTED"
		}
		b, _ := json.Marshal(map[string]any{"ready": false, "code": code})
		fmt.Fprintln(os.Stderr, string(b))
		os.Exit(1)
	}
}
