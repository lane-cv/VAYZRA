package processing

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"image"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const MaxProcessFileSize int64 = 500 * 1024 * 1024
const (
	MaxImageWidth  = 7680
	MaxImageHeight = 4320
	MaxImagePixels = MaxImageWidth * MaxImageHeight
)
const (
	KindDocument = "document"
	KindImage    = "image"
	KindOffice   = "office"
	KindVideo    = "video"
	KindText     = "text"
)

type Detection struct{ MIME, Kind, Extension string }
type typeRule struct{ mime, kind string }

var allowedTypes = map[string]typeRule{
	".pdf": {"application/pdf", KindDocument}, ".jpg": {"image/jpeg", KindImage}, ".jpeg": {"image/jpeg", KindImage}, ".png": {"image/png", KindImage}, ".webp": {"image/webp", KindImage}, ".gif": {"image/gif", KindImage},
	".docx": {"application/vnd.openxmlformats-officedocument.wordprocessingml.document", KindOffice}, ".xlsx": {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", KindOffice}, ".pptx": {"application/vnd.openxmlformats-officedocument.presentationml.presentation", KindOffice},
	".mp4": {"video/mp4", KindVideo}, ".webm": {"video/webm", KindVideo}, ".ogv": {"video/ogg", KindVideo}, ".ogg": {"video/ogg", KindVideo}, ".mov": {"video/quicktime", KindVideo}, ".avi": {"video/x-msvideo", KindVideo}, ".mkv": {"video/x-matroska", KindVideo},
	".txt": {"text/plain", KindText}, ".md": {"text/markdown", KindText},
}
var rejectedExtensions = map[string]bool{".zip": true, ".rar": true, ".7z": true, ".exe": true, ".dll": true, ".svg": true, ".html": true, ".htm": true, ".docm": true, ".xlsm": true, ".pptm": true}

func DetectFile(path, displayName, declaredMIME string, size int64) (Detection, error) {
	if !validDisplayName(displayName) {
		return Detection{}, reject("invalid_name")
	}
	if size < 1 || size > MaxProcessFileSize {
		return Detection{}, reject("file_too_large")
	}
	ext := strings.ToLower(filepath.Ext(displayName))
	if rejectedExtensions[ext] {
		return Detection{}, reject("type_rejected")
	}
	rule, ok := allowedTypes[ext]
	if !ok || declaredMIME != rule.mime {
		return Detection{}, reject("type_rejected")
	}
	file, err := os.Open(path)
	if err != nil {
		return Detection{}, transient("storage_unavailable")
	}
	defer file.Close()
	header := make([]byte, 512)
	n, err := io.ReadFull(file, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return Detection{}, reject("type_rejected")
	}
	header = header[:n]
	if !magicMatches(ext, header) {
		return Detection{}, reject("type_rejected")
	}
	if rule.kind == KindOffice {
		if err := validateOffice(path, ext); err != nil {
			return Detection{}, err
		}
	}
	if rule.kind == KindImage {
		if err := validateImage(path, ext); err != nil {
			return Detection{}, err
		}
	}
	if rule.kind == KindDocument {
		if err := validatePDF(file); err != nil {
			return Detection{}, err
		}
	}
	return Detection{MIME: rule.mime, Kind: rule.kind, Extension: ext}, nil
}

func validatePDF(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return transient("storage_unavailable")
	}
	tailSize := min(info.Size(), 1024)
	tail := make([]byte, tailSize)
	if _, err := file.ReadAt(tail, info.Size()-tailSize); err != nil && !errors.Is(err, io.EOF) {
		return transient("storage_unavailable")
	}
	if !bytes.Contains(tail, []byte("%%EOF")) {
		return reject("malformed_pdf")
	}
	return nil
}

func validateImage(path, ext string) error {
	file, err := os.Open(path)
	if err != nil {
		return transient("storage_unavailable")
	}
	defer file.Close()

	var width, height int
	if ext == ".webp" {
		header := make([]byte, 30)
		n, readErr := io.ReadFull(file, header)
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return reject("malformed_image")
		}
		width, height, err = webPDimensions(header[:n])
	} else {
		config, _, decodeErr := image.DecodeConfig(file)
		if decodeErr != nil {
			return reject("malformed_image")
		}
		width, height = config.Width, config.Height
	}
	if err != nil || width < 1 || height < 1 {
		return reject("malformed_image")
	}
	if width > MaxImageWidth || height > MaxImageHeight || int64(width)*int64(height) > MaxImagePixels {
		return reject("image_dimensions_exceeded")
	}
	if ext == ".gif" {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return transient("storage_unavailable")
		}
		decoded, err := gif.DecodeAll(io.LimitReader(file, MaxProcessFileSize+1))
		if err != nil {
			return reject("malformed_image")
		}
		if len(decoded.Image) != 1 {
			return reject("animated_image_rejected")
		}
	}
	return nil
}

func webPDimensions(header []byte) (int, int, error) {
	if len(header) < 16 || string(header[:4]) != "RIFF" || string(header[8:12]) != "WEBP" {
		return 0, 0, errors.New("invalid WebP header")
	}
	switch string(header[12:16]) {
	case "VP8X":
		if len(header) < 30 {
			return 0, 0, errors.New("short VP8X header")
		}
		width := int(header[24]) | int(header[25])<<8 | int(header[26])<<16
		height := int(header[27]) | int(header[28])<<8 | int(header[29])<<16
		return width + 1, height + 1, nil
	case "VP8 ":
		if len(header) < 30 || !bytes.Equal(header[23:26], []byte{0x9d, 0x01, 0x2a}) {
			return 0, 0, errors.New("invalid VP8 frame")
		}
		return int(binary.LittleEndian.Uint16(header[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(header[28:30]) & 0x3fff), nil
	case "VP8L":
		if len(header) < 25 || header[20] != 0x2f {
			return 0, 0, errors.New("invalid VP8L frame")
		}
		bits := binary.LittleEndian.Uint32(header[21:25])
		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, nil
	default:
		return 0, 0, errors.New("unsupported WebP chunk")
	}
}

func validDisplayName(name string) bool {
	return name != "" && len(name) <= 255 && strings.TrimSpace(name) == name && filepath.Base(name) == name && !strings.ContainsAny(name, "/\\\x00\r\n")
}
func magicMatches(ext string, b []byte) bool {
	switch ext {
	case ".pdf":
		return bytes.HasPrefix(b, []byte("%PDF-"))
	case ".jpg", ".jpeg":
		return len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff
	case ".png":
		return bytes.HasPrefix(b, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	case ".gif":
		return bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a"))
	case ".webp":
		return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
	case ".docx", ".xlsx", ".pptx":
		return bytes.HasPrefix(b, []byte("PK\x03\x04"))
	case ".mp4", ".mov":
		return len(b) >= 12 && string(b[4:8]) == "ftyp"
	case ".webm", ".mkv":
		return bytes.HasPrefix(b, []byte{0x1a, 0x45, 0xdf, 0xa3})
	case ".ogg", ".ogv":
		return bytes.HasPrefix(b, []byte("OggS"))
	case ".avi":
		return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "AVI "
	case ".txt", ".md":
		lower := bytes.ToLower(bytes.TrimSpace(b))
		return utf8.Valid(b) && !bytes.Contains(b, []byte{0}) && !bytes.HasPrefix(lower, []byte("<html")) && !bytes.HasPrefix(lower, []byte("<svg"))
	default:
		return false
	}
}

func validateOffice(path, ext string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return reject("malformed_office")
	}
	defer reader.Close()
	if len(reader.File) == 0 || len(reader.File) > 2048 {
		return reject("malformed_office")
	}
	var contentTypes bool
	var expanded uint64
	wantPart := map[string]string{".docx": "word/document.xml", ".xlsx": "xl/workbook.xml", ".pptx": "ppt/presentation.xml"}[ext]
	var mainPart bool
	for _, file := range reader.File {
		name := strings.ToLower(file.Name)
		if strings.Contains(name, "../") || strings.HasPrefix(name, "/") || strings.Contains(name, "vbaproject.bin") {
			return reject("type_rejected")
		}
		expanded += file.UncompressedSize64
		if expanded > 100*1024*1024 {
			return reject("malformed_office")
		}
		if name == "[content_types].xml" {
			contentTypes = true
		}
		if name == wantPart {
			mainPart = true
		}
		if strings.HasSuffix(name, ".rels") {
			if err := rejectExternalOfficeRelationships(file); err != nil {
				return err
			}
		}
	}
	if !contentTypes || !mainPart {
		return reject("malformed_office")
	}
	return nil
}

func rejectExternalOfficeRelationships(file *zip.File) error {
	if file.UncompressedSize64 > 1024*1024 {
		return reject("malformed_office")
	}
	reader, err := file.Open()
	if err != nil {
		return reject("malformed_office")
	}
	defer reader.Close()
	decoder := xml.NewDecoder(io.LimitReader(reader, 1024*1024+1))
	decoder.Strict = true
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return reject("malformed_office")
		}
		start, ok := token.(xml.StartElement)
		if !ok || !strings.EqualFold(start.Name.Local, "Relationship") {
			continue
		}
		var target, mode string
		for _, attr := range start.Attr {
			switch {
			case strings.EqualFold(attr.Name.Local, "Target"):
				target = strings.TrimSpace(attr.Value)
			case strings.EqualFold(attr.Name.Local, "TargetMode"):
				mode = strings.TrimSpace(attr.Value)
			}
		}
		decodedTarget, decodeErr := url.PathUnescape(target)
		parsedTarget, parseErr := url.Parse(decodedTarget)
		if strings.EqualFold(mode, "External") || decodeErr != nil || parseErr != nil || parsedTarget.IsAbs() || parsedTarget.Host != "" || strings.HasPrefix(decodedTarget, "//") || strings.HasPrefix(decodedTarget, "/") || strings.Contains(decodedTarget, `\`) {
			return reject("office_external_relationship")
		}
	}
}

func reject(category string) error {
	return &ProcessingError{Category: category, Permanent: true, Rejected: true}
}
func transient(category string) error { return &ProcessingError{Category: category} }
