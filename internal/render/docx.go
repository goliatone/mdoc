package render

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	styleElement = regexp.MustCompile(`(?s)<w:style\b.*?</w:style>`)
	styleID      = regexp.MustCompile(`\bw:styleId="([^"]+)"`)
	coreTime     = regexp.MustCompile(`(?s)(<dcterms:(?:created|modified)\b[^>]*>).*?(</dcterms:(?:created|modified)>)`)
)

var requiredStyleIDs = []string{
	"Normal", "BodyText", "FirstParagraph", "Compact", "Title", "Subtitle", "Author", "Date",
	"AbstractTitle", "Abstract", "Bibliography", "Heading1", "Heading2", "Heading3", "Heading4", "Heading5", "Heading6",
	"BlockText", "FootnoteText", "DefinitionTerm", "Definition", "Caption", "TableCaption", "ImageCaption", "Figure",
	"VerbatimChar", "SectionNumber", "FootnoteReference", "Hyperlink",
}

func SupplementReference(authorPath string, defaultReference []byte, outputPath string) error {
	author, err := readZip(authorPath)
	if err != nil {
		return fmt.Errorf("read author reference DOCX: %w", err)
	}
	defaults, err := readZipBytes(defaultReference)
	if err != nil {
		return fmt.Errorf("read default reference DOCX: %w", err)
	}
	authorStyles := author["word/styles.xml"]
	defaultStyles := defaults["word/styles.xml"]
	if len(authorStyles) == 0 || len(defaultStyles) == 0 {
		return errors.New("reference DOCX is missing word/styles.xml")
	}
	author["word/styles.xml"], err = supplementStyles(authorStyles, defaultStyles)
	if err != nil {
		return err
	}
	return writeZip(outputPath, author)
}

func ValidateReference(authorPath string, defaultReference []byte) error {
	author, err := readZip(authorPath)
	if err != nil {
		return fmt.Errorf("read author reference DOCX: %w", err)
	}
	defaults, err := readZipBytes(defaultReference)
	if err != nil {
		return fmt.Errorf("read default reference DOCX: %w", err)
	}
	authorStyles := author["word/styles.xml"]
	defaultStyles := defaults["word/styles.xml"]
	if len(authorStyles) == 0 || len(defaultStyles) == 0 {
		return errors.New("reference DOCX is missing word/styles.xml")
	}
	_, err = supplementStyles(authorStyles, defaultStyles)
	return err
}

func NormalizeDOCX(inputPath, outputPath string) error {
	parts, err := readZip(inputPath)
	if err != nil {
		return err
	}
	if err := ensurePackageContentTypes(parts); err != nil {
		return err
	}
	documentXML := parts["word/document.xml"]
	if len(documentXML) == 0 {
		return errors.New("DOCX is missing word/document.xml")
	}
	if err := auditDOCXConformance(parts, documentXML); err != nil {
		return fmt.Errorf("DOCX conformance audit: %w", err)
	}
	if core := parts["docProps/core.xml"]; len(core) > 0 {
		parts["docProps/core.xml"] = coreTime.ReplaceAll(core, []byte(`${1}2000-01-01T00:00:00Z${2}`))
	}
	return writeZip(outputPath, parts)
}

func supplementStyles(author, defaults []byte) ([]byte, error) {
	known := styleMap(author)
	available := styleMap(defaults)
	missing := []string{}
	for _, id := range requiredStyleIDs {
		if _, ok := known[id]; ok {
			continue
		}
		element, ok := available[id]
		if !ok {
			return nil, fmt.Errorf("pandoc default reference is missing required style %s", id)
		}
		missing = append(missing, element)
	}
	if len(missing) == 0 {
		return append([]byte(nil), author...), nil
	}
	marker := []byte("</w:styles>")
	index := bytes.LastIndex(author, marker)
	if index < 0 {
		return nil, errors.New("reference styles XML has no closing styles element")
	}
	result := make([]byte, 0, len(author)+len(strings.Join(missing, "")))
	result = append(result, author[:index]...)
	result = append(result, []byte(strings.Join(missing, ""))...)
	result = append(result, author[index:]...)
	return result, nil
}

func styleMap(xml []byte) map[string]string {
	result := map[string]string{}
	for _, element := range styleElement.FindAll(xml, -1) {
		match := styleID.FindSubmatch(element)
		if len(match) == 2 {
			result[string(match[1])] = string(element)
		}
	}
	return result
}

func readZip(path string) (map[string][]byte, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	return readZipFiles(archive.File)
}

func readZipBytes(data []byte) (map[string][]byte, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	return readZipFiles(archive.File)
}

func readZipFiles(files []*zip.File) (map[string][]byte, error) {
	result := make(map[string][]byte, len(files))
	for _, file := range files {
		reader, err := file.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			return nil, err
		}
		result[file.Name] = data
	}
	return result, nil
}

func writeZip(path string, parts map[string][]byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	archive := zip.NewWriter(file)
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	sort.Strings(names)
	fixedTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: fixedTime}
		header.SetMode(0o600)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			archive.Close()
			file.Close()
			return err
		}
		if _, err := writer.Write(parts[name]); err != nil {
			archive.Close()
			file.Close()
			return err
		}
	}
	if err := archive.Close(); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
