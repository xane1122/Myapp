package api

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeOfficeFixture(t *testing.T, ext string, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture"+ext)
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(output)
	for name, content := range files {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractOpenXMLTextFromDOCXAndPPTX(t *testing.T) {
	docx := writeOfficeFixture(t, ".docx", map[string]string{
		"word/document.xml": `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>第一段</w:t></w:r></w:p><w:p><w:r><w:t>第二段</w:t></w:r></w:p></w:body></w:document>`,
	})
	docText, err := extractText(docx)
	if err != nil || !strings.Contains(docText, "第一段") || !strings.Contains(docText, "第二段") {
		t.Fatalf("docx text=%q err=%v", docText, err)
	}

	pptx := writeOfficeFixture(t, ".pptx", map[string]string{
		"ppt/slides/slide1.xml": `<p:sld xmlns:p="p" xmlns:a="a"><a:t>标题</a:t><a:t>正文</a:t></p:sld>`,
	})
	pptText, err := extractText(pptx)
	if err != nil || !strings.Contains(pptText, "标题") || !strings.Contains(pptText, "正文") {
		t.Fatalf("pptx text=%q err=%v", pptText, err)
	}
}

func TestExtractOpenXMLTextFromXLSX(t *testing.T) {
	path := writeOfficeFixture(t, ".xlsx", map[string]string{
		"xl/sharedStrings.xml":     `<sst xmlns="x"><si><t>姓名</t></si><si><t>小夏</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet xmlns="x"><sheetData><row><c t="s"><v>0</v></c><c t="s"><v>1</v></c><c><v>42</v></c></row></sheetData></worksheet>`,
	})
	text, err := extractText(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"姓名", "小夏", "42"} {
		if !strings.Contains(text, want) {
			t.Fatalf("xlsx text %q missing %q", text, want)
		}
	}
}

func TestValidateChatDocumentBytesRejectsLegacyAndSpoofedFiles(t *testing.T) {
	if err := validateChatDocumentBytes(".txt", []byte{0xff, 0xfe}); err == nil {
		t.Fatal("expected invalid UTF-8 to fail")
	}
	if err := validateChatDocumentBytes(".pdf", []byte("not a pdf")); err == nil {
		t.Fatal("expected spoofed PDF to fail")
	}
	if _, ok := chatDocumentMIMEs[".doc"]; ok {
		t.Fatal("legacy .doc must not be accepted")
	}
}
