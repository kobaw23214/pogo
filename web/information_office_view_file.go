package web

// Read-only information viewers use a private, immutable disk snapshot. A large
// workbook must not occupy its full compressed size in the Go heap, nor be
// copied in full for every Office-engine fetch.
import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"sync"
)

const informationOfficeViewMaxBytes int64 = 256 << 20
const informationOfficeViewMaxSnapshotBytes int64 = 512 << 20
const informationOfficeViewMaxExpandedBytes uint64 = 4 << 30
const informationOfficeViewMaxPartBytes uint64 = 2 << 30
const informationOfficeViewMaxXMLBytes uint64 = 2 << 30

// The deployed temporary directory can be memory backed. Bound all private
// snapshots together, including opens still copying and fetches finishing
// after their viewer closes. Session-count limits alone cannot bound bytes.
type informationOfficeViewStorageBudget struct {
	mu          sync.Mutex
	limit, used int64
}

var informationOfficeViewStorage = &informationOfficeViewStorageBudget{limit: informationOfficeViewMaxSnapshotBytes}

func (b *informationOfficeViewStorageBudget) reserve(size int64) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if size < 0 || size > b.limit-b.used {
		return nil, officeFail(429, "VIEWER_STORAGE_LIMIT", "只读选区的临时文档空间已达上限，请关闭其他只读选区后重试。")
	}
	b.used += size
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.used -= size
		})
	}, nil
}

type informationOfficeViewSnapshot struct {
	mu             sync.Mutex
	file           *os.File
	name           string
	size           int64
	readers        int
	retired        bool
	releaseStorage func()
}

type informationOfficeViewContextReader struct {
	context context.Context
	reader  io.Reader
}

func (r informationOfficeViewContextReader) Read(p []byte) (int, error) {
	if err := r.context.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func informationOfficeViewPackage(reader io.ReaderAt, size int64, fileType string) error {
	if err := informationOfficeViewZIPPreflight(reader, size); err != nil {
		return err
	}
	archive, err := zip.NewReader(reader, size)
	if err != nil || uint64(len(archive.File)) > informationOfficeViewMaxZIPMembers {
		return officeFail(400, "INVALID_DOCUMENT", "文件不是受支持的 Office 文档。")
	}
	required := "word/document.xml"
	if fileType == "xlsx" {
		required = "xl/workbook.xml"
	}
	found, contentTypes, expanded, xmlBytes := false, false, uint64(0), uint64(0)
	for _, part := range archive.File {
		if part.UncompressedSize64 > informationOfficeViewMaxPartBytes || part.UncompressedSize64 > informationOfficeViewMaxExpandedBytes-expanded {
			return officeFail(413, "DOCUMENT_LIMIT", "Office 只读选区支持单个解压内容不超过 2 GiB、全部解压内容不超过 4 GiB 的文档。")
		}
		expanded += part.UncompressedSize64
		if strings.HasSuffix(strings.ToLower(part.Name), ".xml") {
			if part.UncompressedSize64 > informationOfficeViewMaxXMLBytes-xmlBytes {
				return officeFail(413, "DOCUMENT_LIMIT", "Office 只读选区支持全部 XML 解压内容不超过 2 GiB 的文档。")
			}
			xmlBytes += part.UncompressedSize64
		}
		if part.Name == required {
			found = true
		}
		if part.Name == "[Content_Types].xml" {
			contentTypes = true
		}
	}
	if !found || !contentTypes {
		return officeFail(400, "INVALID_DOCUMENT", "文件不是与扩展名对应的 Office 文档。")
	}
	return nil
}

func informationOfficeViewFreeze(ctx context.Context, source io.Reader, size int64, hash, fileType string) (_ *informationOfficeViewSnapshot, err error) {
	return informationOfficeViewFreezeWithBudget(ctx, source, size, hash, fileType, informationOfficeViewStorage)
}

func informationOfficeViewFreezeWithBudget(ctx context.Context, source io.Reader, size int64, hash, fileType string, budget *informationOfficeViewStorageBudget) (_ *informationOfficeViewSnapshot, err error) {
	if size < 0 || size > informationOfficeViewMaxBytes {
		return nil, officeFail(413, "DOCUMENT_LIMIT", "Office 只读选区支持不超过 256 MiB 的文档，原文件仍保留在信息库。")
	}
	releaseStorage, err := budget.reserve(size)
	if err != nil {
		return nil, err
	}
	var file *os.File
	name := ""
	defer func() {
		if err != nil {
			if file != nil {
				file.Close()
			}
			if name != "" {
				os.Remove(name)
			}
			releaseStorage()
		}
	}()
	file, err = os.CreateTemp("", "pogo-information-office-*")
	if err != nil {
		return nil, err
	}
	name = file.Name()
	digest := sha256.New()
	reader := informationOfficeViewContextReader{ctx, source}
	written, err := io.Copy(io.MultiWriter(file, digest), io.LimitReader(reader, size))
	if err != nil {
		return nil, err
	}
	// Check trailing data without writing beyond the reserved byte count.
	var extra [1]byte
	trailing, extraErr := io.ReadFull(reader, extra[:])
	if extraErr != nil && extraErr != io.EOF {
		return nil, extraErr
	}
	if written != size || trailing != 0 || hex.EncodeToString(digest.Sum(nil)) != hash {
		return nil, officeFail(409, "FILE_IDENTITY_CHANGED", "信息库文档的内容校验失败，请重新选择文档。")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = informationOfficeViewPackage(file, written, fileType); err != nil {
		return nil, err
	}
	// Reopen read-only so neither the serving path nor a later operation can
	// write into the frozen snapshot. On Unix, unlink immediately: even an
	// abrupt process exit cannot leave large temporary documents behind.
	if err = file.Chmod(0400); err != nil {
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	if file, err = os.Open(name); err != nil {
		return nil, err
	}
	if os.Remove(name) == nil {
		name = ""
	}
	return &informationOfficeViewSnapshot{file: file, name: name, size: written, releaseStorage: releaseStorage}, nil
}

// Pin one independent read offset. Closing/expiring the viewer prevents new
// reads immediately; an already authorised engine fetch may finish before its
// last reference closes the file. No document/service lock is held over I/O.
func (f *informationOfficeViewSnapshot) acquire() (io.ReadSeeker, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.retired || f.file == nil {
		return nil, nil, officeFail(409, "SESSION_CLOSED", "只读预览已关闭。")
	}
	f.readers++
	var once sync.Once
	release := func() {
		once.Do(func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.readers--
			f.cleanupLocked()
		})
	}
	return io.NewSectionReader(f.file, 0, f.size), release, nil
}

func (f *informationOfficeViewSnapshot) retire() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retired = true
	f.cleanupLocked()
}

func (f *informationOfficeViewSnapshot) cleanupLocked() {
	if !f.retired || f.readers != 0 || f.file == nil {
		return
	}
	f.file.Close()
	f.file = nil
	if f.name != "" {
		os.Remove(f.name)
		f.name = ""
	}
	if f.releaseStorage != nil {
		f.releaseStorage()
		f.releaseStorage = nil
	}
}
