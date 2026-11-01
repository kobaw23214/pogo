package web

// archive/zip deliberately accepts a truncated EOCD entry count and reads
// central headers until it finds a non-header. Check the actual directory
// before NewReader allocates a File/name/extra/comment for every member.
import (
	"encoding/binary"
	"io"
)

const informationOfficeViewMaxDirectoryBytes uint64 = 16 << 20
const informationOfficeViewMaxZIPMembers uint64 = 4096

type informationOfficeViewZIPEnd struct {
	endOffset                      int64
	directorySize, directoryOffset uint64
	records                        uint64
}

func informationOfficeViewZIPInvalid() error {
	return officeFail(400, "INVALID_DOCUMENT", "文件不是受支持的 Office 文档。")
}

func informationOfficeViewZIPMetadataLimit() error {
	return officeFail(413, "DOCUMENT_LIMIT", "Office 文档内部文件不能超过 4096 个，目录信息不能超过 16 MiB。")
}

func informationOfficeViewZIPRead(reader io.ReaderAt, at int64, size int) ([]byte, error) {
	data := make([]byte, size)
	if _, err := reader.ReadAt(data, at); err != nil {
		return nil, informationOfficeViewZIPInvalid()
	}
	return data, nil
}

func informationOfficeViewZIPDirectoryEnd(reader io.ReaderAt, size int64) (informationOfficeViewZIPEnd, error) {
	var result informationOfficeViewZIPEnd
	if size < 22 {
		return result, informationOfficeViewZIPInvalid()
	}
	var end []byte
	// Match archive/zip's two bounded backwards searches, including its
	// treatment of comments and archives with trailing non-directory bytes.
	for _, amount := range []int64{1024, 65 * 1024} {
		if amount > size {
			amount = size
		}
		tail, err := informationOfficeViewZIPRead(reader, size-amount, int(amount))
		if err != nil {
			return result, err
		}
		for pos := len(tail) - 22; pos >= 0; pos-- {
			if binary.LittleEndian.Uint32(tail[pos:]) != 0x06054b50 {
				continue
			}
			comment := int(binary.LittleEndian.Uint16(tail[pos+20:]))
			if pos+22+comment > len(tail) {
				break
			}
			end = tail[pos : pos+22]
			result.endOffset = size - amount + int64(pos)
			break
		}
		if end != nil {
			break
		}
	}
	if end == nil || binary.LittleEndian.Uint16(end[4:]) != 0 || binary.LittleEndian.Uint16(end[6:]) != 0 {
		return result, informationOfficeViewZIPInvalid()
	}
	result.records = uint64(binary.LittleEndian.Uint16(end[10:]))
	result.directorySize = uint64(binary.LittleEndian.Uint32(end[12:]))
	result.directoryOffset = uint64(binary.LittleEndian.Uint32(end[16:]))
	// ZIP64 stores the real values in a fixed-size end record located through
	// the preceding locator. No allocation uses its untrusted size or counts.
	if result.records == 0xffff || result.directorySize == 0xffff || result.directorySize == 0xffffffff || result.directoryOffset == 0xffffffff {
		if result.endOffset < 20 {
			return result, informationOfficeViewZIPInvalid()
		}
		locator, err := informationOfficeViewZIPRead(reader, result.endOffset-20, 20)
		if err != nil {
			return result, err
		}
		if binary.LittleEndian.Uint32(locator) == 0x07064b50 {
			at := binary.LittleEndian.Uint64(locator[8:])
			if size < 56 || binary.LittleEndian.Uint32(locator[4:]) != 0 || binary.LittleEndian.Uint32(locator[16:]) != 1 || at > uint64(size-56) {
				return result, informationOfficeViewZIPInvalid()
			}
			zip64, err := informationOfficeViewZIPRead(reader, int64(at), 56)
			if err != nil {
				return result, err
			}
			if binary.LittleEndian.Uint32(zip64) != 0x06064b50 || binary.LittleEndian.Uint64(zip64[4:]) < 44 || binary.LittleEndian.Uint32(zip64[16:]) != 0 || binary.LittleEndian.Uint32(zip64[20:]) != 0 {
				return result, informationOfficeViewZIPInvalid()
			}
			result.endOffset = int64(at)
			result.records = binary.LittleEndian.Uint64(zip64[32:])
			result.directorySize = binary.LittleEndian.Uint64(zip64[40:])
			result.directoryOffset = binary.LittleEndian.Uint64(zip64[48:])
		}
	}
	if result.records > informationOfficeViewMaxZIPMembers || result.directorySize > informationOfficeViewMaxDirectoryBytes {
		return result, informationOfficeViewZIPMetadataLimit()
	}
	if result.directorySize > uint64(result.endOffset) || result.directoryOffset >= uint64(size) {
		return result, informationOfficeViewZIPInvalid()
	}
	return result, nil
}

// Count physical headers, independently of the EOCD's possibly forged count
// and byte size. Only fixed 46-byte headers are read; variable fields are
// skipped after their lengths pass the aggregate budget.
func informationOfficeViewZIPWalk(reader io.ReaderAt, size, start int64) error {
	at, records, bytes := start, uint64(0), uint64(0)
	var fixed [46]byte
	for at >= 0 && at <= size-int64(len(fixed)) {
		if _, err := reader.ReadAt(fixed[:], at); err != nil {
			return informationOfficeViewZIPInvalid()
		}
		if binary.LittleEndian.Uint32(fixed[:]) != 0x02014b50 {
			return nil
		}
		length := uint64(len(fixed)) + uint64(binary.LittleEndian.Uint16(fixed[28:])) + uint64(binary.LittleEndian.Uint16(fixed[30:])) + uint64(binary.LittleEndian.Uint16(fixed[32:]))
		records++
		if records > informationOfficeViewMaxZIPMembers || length > informationOfficeViewMaxDirectoryBytes-bytes {
			return informationOfficeViewZIPMetadataLimit()
		}
		if length > uint64(size-at) {
			return informationOfficeViewZIPInvalid()
		}
		bytes += length
		at += int64(length)
	}
	return nil
}

func informationOfficeViewZIPPreflight(reader io.ReaderAt, size int64) error {
	end, err := informationOfficeViewZIPDirectoryEnd(reader, size)
	if err != nil {
		return err
	}
	start := end.endOffset - int64(end.directorySize)
	if err := informationOfficeViewZIPWalk(reader, size, start); err != nil {
		return err
	}
	// archive/zip supports prepended data and a compatibility fallback to the
	// unadjusted directory offset. Bound both possible starts before letting
	// it choose: a forged directory size must not hide an oversized directory.
	if start > int64(end.directoryOffset) {
		return informationOfficeViewZIPWalk(reader, size, int64(end.directoryOffset))
	}
	return nil
}
