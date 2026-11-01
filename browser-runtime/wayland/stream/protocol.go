package main

import (
	"bufio"
	"bytes"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

const maxJSON = 65536
const maxPayload = 8 << 20

var identifier = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
var sessionIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)
var profilePattern = regexp.MustCompile(`^[0-9a-fA-F]{6}$`)
var errInvalid = errors.New("INVALID_REQUEST")
var errScope = errors.New("SCOPE_MISMATCH")
var errCapacity = errors.New("CAPACITY_REACHED")
var errMedia = errors.New("MEDIA_FAILED")

type scope struct {
	Session    string `json:"session"`
	Page       string `json:"page"`
	View       string `json:"view"`
	Epoch      uint64 `json:"epoch"`
	Generation uint64 `json:"generation"`
}

func (s scope) valid() bool {
	return sessionIdentifier.MatchString(s.Session) && identifier.MatchString(s.Page) && identifier.MatchString(s.View) && s.Epoch > 0 && s.Generation > 0
}

type request struct {
	ID   string          `json:"id"`
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args"`
}
type hello struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
	Session string `json:"session,omitempty"`
	Token   string `json:"token"`
}
type frameHeader struct {
	Codec         string `json:"codec"`
	Page          string `json:"page"`
	Display       uint32 `json:"display_id"`
	Generation    uint64 `json:"generation"`
	Seq           uint64 `json:"seq"`
	ContentSeq    uint64 `json:"content_seq"`
	CaptureNS     uint64 `json:"capture_ns"`
	EncodeNS      uint64 `json:"encode_ns,omitempty"`
	EncodeStartNS uint64 `json:"encode_start_ns,omitempty"`
	EncodeEndNS   uint64 `json:"encode_end_ns,omitempty"`
	Width         uint16 `json:"width"`
	Height        uint16 `json:"height"`
	Keyframe      bool   `json:"keyframe"`
	FrameID       int32  `json:"frame_id"`
	ReferenceID   int32  `json:"reference_frame_id"`
	PayloadBytes  uint32 `json:"payload_bytes"`
	Profile       string `json:"profile_level_id,omitempty"`
}
type frame struct {
	frameHeader
	Payload []byte
}

func secretEqual(a, b string) bool {
	return len(a) >= 32 && len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func strictJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return errInvalid
	}
	if d.Decode(new(any)) != io.EOF {
		return errInvalid
	}
	return nil
}
func readLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadSlice('\n')
	if err != nil {
		return nil, err
	}
	if len(line) > maxJSON {
		return nil, errInvalid
	}
	return line, nil
}
func readFrame(r io.Reader) (*frame, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(n[:])
	if length == 0 || length > maxJSON {
		return nil, errInvalid
	}
	b := make([]byte, length)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	f := new(frame)
	if strictJSON(b, &f.frameHeader) != nil {
		return nil, errInvalid
	}
	if !identifier.MatchString(f.Page) || f.Display < 2 || f.Generation == 0 || f.Seq == 0 || f.CaptureNS == 0 || f.PayloadBytes == 0 || f.PayloadBytes > maxPayload || f.Width < 2 || f.Height < 2 || f.Width > 3840 || f.Height > 2400 || f.Width%2 != 0 || f.Height%2 != 0 || uint64(f.Width)*uint64(f.Height) > 7464960 || (f.Profile != "" && !profilePattern.MatchString(f.Profile)) {
		return nil, errInvalid
	}
	if (f.EncodeStartNS == 0) != (f.EncodeEndNS == 0) || f.EncodeEndNS < f.EncodeStartNS {
		return nil, errInvalid
	}
	f.Payload = make([]byte, f.PayloadBytes)
	if _, err := io.ReadFull(r, f.Payload); err != nil {
		return nil, err
	}
	switch f.Codec {
	case "h264":
		idr, sps, pps := false, false, false
		for _, nal := range annexB(f.Payload) {
			switch nal[0] & 31 {
			case 5:
				idr = true
			case 7:
				sps = true
			case 8:
				pps = true
			}
		}
		if idr != f.Keyframe || (idr && (!sps || !pps)) {
			return nil, errInvalid
		}
	case "vp8":
		if len(f.Payload) < 3 {
			return nil, errInvalid
		}
		key := f.Payload[0]&1 == 0
		if key != f.Keyframe {
			return nil, errInvalid
		}
		if key && (len(f.Payload) < 10 || !bytes.Equal(f.Payload[3:6], []byte{0x9d, 0x01, 0x2a}) || binary.LittleEndian.Uint16(f.Payload[6:8])&0x3fff != f.Width || binary.LittleEndian.Uint16(f.Payload[8:10])&0x3fff != f.Height) {
			return nil, errInvalid
		}
	default:
		return nil, errInvalid
	}
	return f, nil
}
func annexB(b []byte) [][]byte {
	var out [][]byte
	start := -1
	for i := 0; i+3 <= len(b); i++ {
		n := 0
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			n = 3
		} else if i+4 <= len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 0 && b[i+3] == 1 {
			n = 4
		}
		if n == 0 {
			continue
		}
		if start >= 0 && i > start {
			out = append(out, b[start:i])
		}
		start = i + n
		i += n - 1
	}
	if start >= 0 && start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
