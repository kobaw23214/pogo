package web

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

func cleanDownloadFailureCode(code string) string {
	switch code {
	case "DOWNLOAD_TOO_LARGE", "DOWNLOAD_TOTAL_LIMIT", "DOWNLOAD_CANCELED", "DOWNLOAD_TIMEOUT", "DOWNLOAD_EXPIRED", "DOWNLOAD_FILE_UNAVAILABLE":
		return code
	default:
		return "DOWNLOAD_FAILED"
	}
}
func permanentDownloadRejection(code string) bool {
	switch code {
	case "ATTACHMENT_REJECTED", "DOWNLOAD_TOO_LARGE", "DOWNLOAD_EMPTY", "DOWNLOAD_TYPE_NOT_ALLOWED", "DOWNLOAD_INVALID_FILE", "DOWNLOAD_SIZE_MISMATCH", "DOWNLOAD_FILE_UNAVAILABLE":
		return true
	default:
		return false
	}
}
func downloadByteLabel(n int64) string {
	if n >= 1<<20 {
		if n%(1<<20) == 0 {
			return fmt.Sprintf("%d MiB", n>>20)
		}
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d 字节", max(0, n))
}

// Error codes are a closed vocabulary. Never forward site/worker messages that
// could carry credentials, private URLs or instructions into the conversation.
func downloadFailureMessage(code string, actual, limit int64) string {
	switch code {
	case "DOWNLOAD_TOO_LARGE":
		if actual > 0 {
			return fmt.Sprintf("文件大小 %s（%d 字节），超过单个下载 %s（%d 字节）上限。", downloadByteLabel(actual), actual, downloadByteLabel(limit), limit)
		}
		return fmt.Sprintf("文件超过单个下载 %s 上限。", downloadByteLabel(limit))
	case "DOWNLOAD_TOTAL_LIMIT":
		return fmt.Sprintf("浏览器下载暂存已占用或预留 %s（%d 字节），超过总量 %s（%d 字节）上限。", downloadByteLabel(actual), actual, downloadByteLabel(limit), limit)
	case "DOWNLOAD_CANCELED":
		return "浏览器已取消下载，或下载连接被中断。"
	case "DOWNLOAD_TIMEOUT":
		return "下载超过五分钟仍未完成，已取消。"
	case "DOWNLOAD_EXPIRED":
		return "下载文件已超过暂存期限，未能加入本会话。"
	case "DOWNLOAD_FILE_UNAVAILABLE":
		return "浏览器下载文件已不可用，未能加入本会话。"
	case "DOWNLOAD_EMPTY":
		return "下载文件为空，未加入本会话。"
	case "DOWNLOAD_TYPE_NOT_ALLOWED":
		return "下载文件类型不在当前允许列表中，未加入本会话。"
	case "DOWNLOAD_INVALID_FILE":
		return "下载内容与文件类型不匹配，或文件不完整，未加入本会话。"
	case "DOWNLOAD_SIZE_MISMATCH":
		return "下载文件大小校验失败，未加入本会话。"
	default:
		return "浏览器下载失败或已取消。"
	}
}

// Inspect a fixed prefix or bounded ZIP directory through ReaderAt, never a
// byte slice proportional to the download. Upload classification is unchanged.
func classifyBrowserDownloadFile(name string, file *os.File, size int64, allowed map[string]bool) (AttachmentMeta, string, string, error) {
	meta := AttachmentMeta{Size: size, Created: time.Now()}
	ext := strings.ToLower(filepath.Ext(name))
	prefix := make([]byte, min(size, 1024))
	if _, err := file.ReadAt(prefix, 0); err != nil {
		return AttachmentMeta{}, "", "", err
	}
	sniffed := http.DetectContentType(prefix)
	invalid := func() (AttachmentMeta, string, string, error) {
		return AttachmentMeta{}, "", "", &browserDownloadRejected{code: "DOWNLOAD_INVALID_FILE"}
	}
	if imageExt, ok := attachImageExts[sniffed]; ok && allowed[imageExt] {
		meta.Kind, meta.Mime, ext = "image", sniffed, imageExt
		if cfg, _, err := image.DecodeConfig(io.NewSectionReader(file, 0, min(size, 1<<20))); err == nil {
			meta.W, meta.H = cfg.Width, cfg.Height
		}
	} else if kind, ok := attachDocExts[ext]; ok && allowed[ext] {
		meta.Kind, meta.Mime = kind, sniffed
		switch ext {
		case ".xlsx", ".docx":
			if !validDownloadOfficeZIP(file, size, ext) {
				return invalid()
			}
			if ext == ".xlsx" {
				meta.Mime = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
			} else {
				meta.Mime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
			}
		case ".pdf":
			if !bytes.HasPrefix(bytes.TrimSpace(bytes.TrimPrefix(prefix, []byte{0xef, 0xbb, 0xbf})), []byte("%PDF-")) {
				return invalid()
			}
			meta.Mime = "application/pdf"
		case ".doc":
			if !bytes.HasPrefix(prefix, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
				return invalid()
			}
		case ".csv", ".tsv":
			if !strings.HasPrefix(sniffed, "text/plain") {
				return invalid()
			}
		}
	} else if attachTextExts[ext] && allowed[ext] {
		if sniffed == "application/octet-stream" {
			return invalid()
		}
		meta.Kind, meta.Mime = "text", sniffed
	} else if attachBinaryExts[ext] && allowed[ext] {
		meta.Kind, meta.Mime = "binary", sniffed
	} else {
		return AttachmentMeta{}, "", "", &browserDownloadRejected{code: "DOWNLOAD_TYPE_NOT_ALLOWED"}
	}
	base := sanitizeName(strings.TrimSuffix(path.Base(name), filepath.Ext(name)))
	if base == "" {
		base = "attachment"
	}
	return meta, base, ext, nil
}

// Bound ZIP metadata before archive/zip allocates entries. Millions of empty
// records inside a 512 MiB file must not exhaust the app's memory.
func validDownloadOfficeZIP(file io.ReaderAt, size int64, ext string) bool {
	const maxDirectory = 8 << 20
	const maxEntries = 10000
	const maxTypesXML = 256 << 10
	if size < 22 {
		return false
	}
	tail := make([]byte, min(size, 65*1024))
	if _, err := file.ReadAt(tail, size-int64(len(tail))); err != nil {
		return false
	}
	end := -1
	for n := len(tail) - 22; n >= 0; n-- {
		if bytes.Equal(tail[n:n+4], []byte{'P', 'K', 5, 6}) {
			// Match archive/zip's last-signature choice, rejecting trailing or
			// truncated comments rather than selecting an earlier outer EOCD.
			if n+22+int(binary.LittleEndian.Uint16(tail[n+20:n+22])) != len(tail) {
				return false
			}
			end = n
			break
		}
	}
	if end < 0 {
		return false
	}
	if end >= 20 && bytes.Equal(tail[end-20:end-16], []byte{'P', 'K', 6, 7}) {
		return false
	}
	entry := tail[end:]
	count := binary.LittleEndian.Uint16(entry[10:12])
	directorySize := int64(binary.LittleEndian.Uint32(entry[12:16]))
	directoryOffset := int64(binary.LittleEndian.Uint32(entry[16:20]))
	if binary.LittleEndian.Uint16(entry[4:6]) != 0 || binary.LittleEndian.Uint16(entry[6:8]) != 0 || binary.LittleEndian.Uint16(entry[8:10]) != count || count == 0 || count > maxEntries || directorySize == 0xffff || directorySize > maxDirectory || directoryOffset+directorySize != size-int64(len(tail))+int64(end) {
		return false
	}
	// archive/zip does not enforce the advertised directorySize while reading.
	// Scan the actual records first so forged counts cannot allocate past it.
	directory := io.NewSectionReader(file, directoryOffset, directorySize)
	var header [46]byte
	var scanned int64
	for record := 0; record < int(count); record++ {
		if _, err := io.ReadFull(directory, header[:]); err != nil || !bytes.Equal(header[:4], []byte{'P', 'K', 1, 2}) {
			return false
		}
		length := int64(binary.LittleEndian.Uint16(header[28:30])) + int64(binary.LittleEndian.Uint16(header[30:32])) + int64(binary.LittleEndian.Uint16(header[32:34]))
		scanned += 46 + length
		if scanned > directorySize {
			return false
		}
		if _, err := directory.Seek(length, io.SeekCurrent); err != nil {
			return false
		}
	}
	if scanned != directorySize {
		return false
	}
	archive, err := zip.NewReader(file, size)
	if err != nil || len(archive.File) != int(count) {
		return false
	}
	mainName, mainType := "xl/workbook.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"
	if ext == ".docx" {
		mainName, mainType = "word/document.xml", "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"
	}
	required := map[string]*zip.File{"[Content_Types].xml": nil, "_rels/.rels": nil, mainName: nil}
	for _, entry := range archive.File {
		if _, ok := required[entry.Name]; !ok {
			continue
		}
		if required[entry.Name] != nil || entry.Flags&1 != 0 || entry.UncompressedSize64 == 0 {
			return false
		}
		required[entry.Name] = entry
	}
	for _, entry := range required {
		if entry == nil {
			return false
		}
	}
	types := required["[Content_Types].xml"]
	if types.UncompressedSize64 > maxTypesXML {
		return false
	}
	reader, err := types.Open()
	if err != nil {
		return false
	}
	defer reader.Close()
	decoder := xml.NewDecoder(io.LimitReader(reader, maxTypesXML+1))
	found := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return found && decoder.InputOffset() <= maxTypesXML
		}
		if err != nil || decoder.InputOffset() > maxTypesXML {
			return false
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "Override" {
			part, content := "", ""
			for _, attr := range start.Attr {
				if attr.Name.Local == "PartName" {
					part = attr.Value
				}
				if attr.Name.Local == "ContentType" {
					content = attr.Value
				}
			}
			if part == "/"+mainName && content == mainType {
				found = true
			}
		}
	}
}
