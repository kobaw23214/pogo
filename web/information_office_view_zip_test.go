package web

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

type informationViewCountedReaderAt struct {
	reader io.ReaderAt
	bytes  int
}

func (r *informationViewCountedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.reader.ReadAt(p, off)
	r.bytes += n
	return n, err
}

func informationViewZIPAddCentralMembers(t *testing.T, original []byte, extra int, forgedSize bool) []byte {
	t.Helper()
	endAt := len(original) - 22
	if endAt < 0 || binary.LittleEndian.Uint32(original[endAt:]) != 0x06054b50 {
		t.Fatal("fixture does not have a simple ZIP directory end")
	}
	end := append([]byte(nil), original[endAt:]...)
	var header [47]byte
	binary.LittleEndian.PutUint32(header[:], 0x02014b50)
	binary.LittleEndian.PutUint16(header[4:], 20)
	binary.LittleEndian.PutUint16(header[6:], 20)
	binary.LittleEndian.PutUint16(header[28:], 1)
	header[46] = 'd'
	result := append([]byte(nil), original[:endAt]...)
	for i := 0; i < extra; i++ {
		result = append(result, header[:]...)
	}
	count := uint16(uint64(binary.LittleEndian.Uint16(end[10:])) + uint64(extra))
	binary.LittleEndian.PutUint16(end[8:], count)
	binary.LittleEndian.PutUint16(end[10:], count)
	size := binary.LittleEndian.Uint32(end[12:]) + uint32(extra*len(header))
	if forgedSize {
		size = 46
	}
	binary.LittleEndian.PutUint32(end[12:], size)
	return append(result, end...)
}

func informationViewZIP64(t *testing.T, original []byte) []byte {
	t.Helper()
	endAt := len(original) - 22
	if endAt < 0 || binary.LittleEndian.Uint32(original[endAt:]) != 0x06054b50 {
		t.Fatal("fixture does not have a simple ZIP directory end")
	}
	legacy := append([]byte(nil), original[endAt:]...)
	var end [56]byte
	binary.LittleEndian.PutUint32(end[:], 0x06064b50)
	binary.LittleEndian.PutUint64(end[4:], 44)
	binary.LittleEndian.PutUint16(end[12:], 45)
	binary.LittleEndian.PutUint16(end[14:], 45)
	count := uint64(binary.LittleEndian.Uint16(legacy[10:]))
	binary.LittleEndian.PutUint64(end[24:], count)
	binary.LittleEndian.PutUint64(end[32:], count)
	binary.LittleEndian.PutUint64(end[40:], uint64(binary.LittleEndian.Uint32(legacy[12:])))
	binary.LittleEndian.PutUint64(end[48:], uint64(binary.LittleEndian.Uint32(legacy[16:])))
	var locator [20]byte
	binary.LittleEndian.PutUint32(locator[:], 0x07064b50)
	binary.LittleEndian.PutUint64(locator[8:], uint64(endAt))
	binary.LittleEndian.PutUint32(locator[16:], 1)
	binary.LittleEndian.PutUint16(legacy[8:], 0xffff)
	binary.LittleEndian.PutUint16(legacy[10:], 0xffff)
	binary.LittleEndian.PutUint32(legacy[12:], 0xffffffff)
	binary.LittleEndian.PutUint32(legacy[16:], 0xffffffff)
	result := append([]byte(nil), original[:endAt]...)
	result = append(result, end[:]...)
	result = append(result, locator[:]...)
	return append(result, legacy...)
}

func TestInformationOfficeViewZIPMetadataPreflightBoundsRealAndForgedMemberCounts(t *testing.T) {
	original := officeTestPackage(t, "bounded central directory")
	for _, test := range []struct {
		name       string
		extra      int
		forgedSize bool
	}{
		{"declared too many", 4096, false},
		// The legacy count wraps modulo 65536, exactly what archive/zip accepts.
		{"wrapped low count", 65536, false},
		{"wrapped low count and forged directory size", 65536, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := informationViewZIPAddCentralMembers(t, original, test.extra, test.forgedSize)
			reader := &informationViewCountedReaderAt{reader: bytes.NewReader(data)}
			err := informationOfficeViewPackage(reader, int64(len(data)), "docx")
			var failure *officeError
			if !errors.As(err, &failure) || failure.status != 413 || failure.code != "DOCUMENT_LIMIT" {
				t.Fatal("unsafe directory did not hit preallocation budget", err)
			}
			if reader.bytes > 512<<10 {
				t.Fatal("oversized directory was read before being bounded", reader.bytes)
			}
		})
	}
}

func TestInformationOfficeViewZIPMetadataPreflightAcceptsOfficeZIP64AndPrefix(t *testing.T) {
	word := officeTestPackage(t, "valid ZIP variants")
	zip64 := informationViewZIP64(t, word)
	commented := append([]byte(nil), word...)
	binary.LittleEndian.PutUint16(commented[len(commented)-2:], 60000)
	commented = append(commented, bytes.Repeat([]byte{'c'}, 60000)...)
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"ordinary Office", word},
		{"explicit ZIP64 end", zip64},
		{"prepended data", append([]byte("valid prepended ZIP payload\n"), word...)},
		{"long ZIP comment", commented},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := zip.NewReader(bytes.NewReader(test.data), int64(len(test.data))); err != nil {
				t.Fatal("invalid compatibility fixture", err)
			}
			if err := informationOfficeViewPackage(bytes.NewReader(test.data), int64(len(test.data)), "docx"); err != nil {
				t.Fatal("valid Office ZIP was rejected", err)
			}
		})
	}
}

func TestInformationOfficeViewZIPMetadataPreflightCountsActualVariableDirectoryBytes(t *testing.T) {
	original := officeTestPackage(t, "bounded directory fields")
	endAt := len(original) - 22
	legacy := append([]byte(nil), original[endAt:]...)
	const extra = 3000
	var header [46]byte
	binary.LittleEndian.PutUint32(header[:], 0x02014b50)
	binary.LittleEndian.PutUint16(header[4:], 20)
	binary.LittleEndian.PutUint16(header[6:], 20)
	binary.LittleEndian.PutUint16(header[28:], 6000)
	longName := bytes.Repeat([]byte{'d'}, 6000)
	data := append([]byte(nil), original[:endAt]...)
	for i := 0; i < extra; i++ {
		data = append(data, header[:]...)
		data = append(data, longName...)
	}
	count := binary.LittleEndian.Uint16(legacy[10:]) + extra
	binary.LittleEndian.PutUint16(legacy[8:], count)
	binary.LittleEndian.PutUint16(legacy[10:], count)
	// A small forged size cannot redirect preflight away from the actual
	// long-name directory that archive/zip's offset fallback would choose.
	binary.LittleEndian.PutUint32(legacy[12:], 46)
	data = append(data, legacy...)
	reader := &informationViewCountedReaderAt{reader: bytes.NewReader(data)}
	err := informationOfficeViewPackage(reader, int64(len(data)), "docx")
	var failure *officeError
	if !errors.As(err, &failure) || failure.status != 413 || reader.bytes > 512<<10 {
		t.Fatal("actual name/extra/comment metadata escaped its byte budget", err, reader.bytes)
	}
}

func TestInformationOfficeViewZIPMetadataPreflightRejectsZIP64CountsAndDirectoryBytes(t *testing.T) {
	original := officeTestPackage(t, "bounded ZIP64 metadata")
	for _, test := range []struct {
		name   string
		mutate func([]byte)
	}{
		{"ZIP64 record count", func(data []byte) {
			at := len(original) - 22
			binary.LittleEndian.PutUint64(data[at+24:], 65538)
			binary.LittleEndian.PutUint64(data[at+32:], 65538)
		}},
		{"ZIP64 directory bytes", func(data []byte) {
			at := len(original) - 22
			binary.LittleEndian.PutUint64(data[at+40:], informationOfficeViewMaxDirectoryBytes+1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := informationViewZIP64(t, original)
			test.mutate(data)
			reader := &informationViewCountedReaderAt{reader: bytes.NewReader(data)}
			err := informationOfficeViewPackage(reader, int64(len(data)), "docx")
			var failure *officeError
			if !errors.As(err, &failure) || failure.status != 413 || reader.bytes > 128<<10 {
				t.Fatal("untrusted ZIP64 metadata was not bounded before parsing", err, reader.bytes)
			}
		})
	}
}
