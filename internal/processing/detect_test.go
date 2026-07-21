package processing

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectFileAcceptsMatchingSafeTypes(t *testing.T) {
	validPNG := encodeImage(t, "png", 2, 2)
	for _, tc := range []struct {
		name, mime string
		body       []byte
		want       string
	}{
		{"lesson.pdf", "application/pdf", []byte("%PDF-1.7\n%%EOF\n"), "application/pdf"},
		{"plot.png", "image/png", validPNG, "image/png"},
		{"notes.txt", "text/plain", []byte("Newton laws\n"), "text/plain"},
		{"lesson.md", "text/markdown", []byte("# Motion\n"), "text/markdown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFixture(t, tc.body)
			got, err := DetectFile(path, tc.name, tc.mime, int64(len(tc.body)))
			if err != nil || got.MIME != tc.want {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

func TestDetectFileRejectsDangerousAndMismatchedTypes(t *testing.T) {
	for _, tc := range []struct {
		name, mime string
		body       []byte
	}{
		{"lesson.pdf.exe", "application/pdf", []byte("MZpayload")},
		{"archive.zip", "application/zip", []byte("PK\x03\x04")},
		{"macro.docm", "application/vnd.ms-word.document.macroEnabled.12", []byte("PK\x03\x04")},
		{"diagram.svg", "image/svg+xml", []byte("<svg></svg>")},
		{"lesson.pdf", "application/pdf", []byte("<html>not pdf</html>")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DetectFile(writeFixture(t, tc.body), tc.name, tc.mime, int64(len(tc.body)))
			if category(err) != "type_rejected" {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestDetectFileAcceptsNonMacroOfficeAndRejectsMalformedOfficeZip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "office")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range map[string]string{"[Content_Types].xml": `<Types><Override ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`, "word/document.xml": "<document/>"} {
		entry, e := zw.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = entry.Write([]byte(body)); e != nil {
			t.Fatal(e)
		}
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := DetectFile(path, "lesson.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", fileSize(t, path))
	if err != nil || got.Kind != KindOffice {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	bad := writeFixture(t, []byte("PK\x03\x04broken"))
	if _, err = DetectFile(bad, "bad.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", fileSize(t, bad)); category(err) != "malformed_office" {
		t.Fatalf("err=%v", err)
	}
}

func TestDetectFileRejectsOfficeExternalRelationships(t *testing.T) {
	for _, relationship := range []string{
		`<Relationship TargetMode="External" Target="http://postgres:5432/"/>`,
		`<Relationship Target="http:/postgres:5432/"/>`,
		`<Relationship Target="http%3A%2Fpostgres:5432/"/>`,
	} {
		path := filepath.Join(t.TempDir(), "office")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(f)
		for name, body := range map[string]string{
			"[Content_Types].xml":          `<Types/>`,
			"word/document.xml":            `<document/>`,
			"word/_rels/document.xml.rels": `<Relationships>` + relationship + `</Relationships>`,
		} {
			entry, createErr := zw.Create(name)
			if createErr != nil {
				t.Fatal(createErr)
			}
			if _, writeErr := entry.Write([]byte(body)); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
		if err = zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = DetectFile(path, "lesson.docx", allowedTypes[".docx"].mime, fileSize(t, path))
		if category(err) != "office_external_relationship" {
			t.Fatalf("relationship=%q err=%v", relationship, err)
		}
	}
}

func TestDetectFileRejectsOversizeAndTraversalNames(t *testing.T) {
	path := writeFixture(t, []byte("text"))
	for _, name := range []string{"../lesson.txt", "folder/lesson.txt", " lesson.txt", strings.Repeat("x", 256) + ".txt"} {
		if _, err := DetectFile(path, name, "text/plain", 4); category(err) != "invalid_name" {
			t.Fatalf("name=%q err=%v", name, err)
		}
	}
	if _, err := DetectFile(path, "lesson.txt", "text/plain", MaxProcessFileSize+1); category(err) != "file_too_large" {
		t.Fatalf("err=%v", err)
	}
}

func TestDetectFileValidatesImageStructureDimensionsAndAnimation(t *testing.T) {
	validPNG := encodeImage(t, "png", 2, 2)
	validJPEG := encodeImage(t, "jpeg", 2, 2)
	for _, tc := range []struct {
		name, mime string
		body       []byte
	}{
		{"plot.png", "image/png", validPNG},
		{"photo.jpg", "image/jpeg", validJPEG},
	} {
		got, err := DetectFile(writeFixture(t, tc.body), tc.name, tc.mime, int64(len(tc.body)))
		if err != nil || got.Kind != KindImage {
			t.Fatalf("name=%s got=%+v err=%v", tc.name, got, err)
		}
	}

	malformed := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 32)...)
	if _, err := DetectFile(writeFixture(t, malformed), "bad.png", "image/png", int64(len(malformed))); category(err) != "malformed_image" {
		t.Fatalf("malformed image err=%v", err)
	}

	tooWide := encodeImage(t, "png", MaxImageWidth+1, 1)
	if _, err := DetectFile(writeFixture(t, tooWide), "wide.png", "image/png", int64(len(tooWide))); category(err) != "image_dimensions_exceeded" {
		t.Fatalf("oversized dimensions err=%v", err)
	}

	animated := &gif.GIF{
		Image: []*image.Paletted{
			image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black}),
			image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.White}),
		},
		Delay: []int{1, 1},
	}
	var encoded bytes.Buffer
	if err := gif.EncodeAll(&encoded, animated); err != nil {
		t.Fatal(err)
	}
	if _, err := DetectFile(writeFixture(t, encoded.Bytes()), "animated.gif", "image/gif", int64(encoded.Len())); category(err) != "animated_image_rejected" {
		t.Fatalf("animated GIF err=%v", err)
	}
}

func TestDetectFileRejectsStructurallyIncompletePDF(t *testing.T) {
	body := []byte("%PDF-1.7\n1 0 obj\n")
	if _, err := DetectFile(writeFixture(t, body), "broken.pdf", "application/pdf", int64(len(body))); category(err) != "malformed_pdf" {
		t.Fatalf("err=%v", err)
	}
}

func encodeImage(t *testing.T, format string, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var encoded bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&encoded, img)
	case "jpeg":
		err = jpeg.Encode(&encoded, img, nil)
	default:
		t.Fatalf("unsupported test format %q", format)
	}
	if err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func writeFixture(t *testing.T, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}
func category(err error) string {
	if e, ok := err.(*ProcessingError); ok {
		return e.Category
	}
	return ""
}
