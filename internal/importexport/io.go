package importexport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/yuin/goldmark"

	"github.com/cyrusaf/agentpad/internal/domain"
)

type Imported struct {
	Title        string
	Format       domain.DocumentFormat
	Source       string
	Warnings     []string
	SourcePath   string
	SourceFormat domain.DocumentFormat
}

func ImportFile(path string) (Imported, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Imported{}, err
	}
	return ImportData(filepath.Base(path), data, path)
}

func ImportData(name string, data []byte, sourcePath string) (Imported, error) {
	ext := strings.ToLower(filepath.Ext(name))
	title := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	source := normalizeNewlines(string(data))
	switch ext {
	case ".md", ".markdown":
		return Imported{Title: title, Format: domain.DocumentFormatMarkdown, Source: source, SourcePath: sourcePath, SourceFormat: domain.DocumentFormatMarkdown}, nil
	case ".txt":
		return Imported{Title: title, Format: domain.DocumentFormatText, Source: source, SourcePath: sourcePath, SourceFormat: domain.DocumentFormatText}, nil
	case ".html", ".htm":
		md, err := htmltomarkdown.ConvertString(source)
		if err != nil {
			return Imported{}, domain.NewError(domain.ErrCodeUnsupportedImport, err.Error(), 400)
		}
		return Imported{
			Title:        title,
			Format:       domain.DocumentFormatMarkdown,
			Source:       normalizeNewlines(md),
			Warnings:     []string{"HTML was normalized into Markdown for collaborative editing."},
			SourcePath:   sourcePath,
			SourceFormat: domain.DocumentFormatHTML,
		}, nil
	default:
		return Imported{Title: title, Format: domain.DocumentFormatCode, Source: source, SourcePath: sourcePath, SourceFormat: domain.DocumentFormatCode}, nil
	}
}

func ExportDocument(doc domain.Document, format domain.DocumentFormat) ([]byte, string, error) {
	switch format {
	case domain.DocumentFormatMarkdown, domain.DocumentFormatText, domain.DocumentFormatCode:
		return []byte(doc.Source), string(format), nil
	case domain.DocumentFormatHTML:
		var buf bytes.Buffer
		if err := goldmark.Convert([]byte(doc.Source), &buf); err != nil {
			return nil, "", domain.NewError(domain.ErrCodeExportFailure, err.Error(), 500)
		}
		return buf.Bytes(), "html", nil
	case domain.DocumentFormatJSON:
		body, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, "", domain.NewError(domain.ErrCodeExportFailure, err.Error(), 500)
		}
		return body, "json", nil
	default:
		return nil, "", domain.NewError(domain.ErrCodeExportFailure, fmt.Sprintf("unsupported export format %q", format), 400)
	}
}

func normalizeNewlines(source string) string {
	return strings.ReplaceAll(source, "\r\n", "\n")
}
