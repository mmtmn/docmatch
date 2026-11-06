package workspace

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// ParsePaths parses terminal drag/drop text as data, never as shell commands.
// Quoted paths, escaped spaces, multiple lines and file: URLs are supported.
func ParsePaths(value string) ([]string, error) {
	var out []string
	var word strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if word.Len() > 0 {
			out = append(out, word.String())
			word.Reset()
		}
	}
	runes := []rune(value)
	for i, r := range runes {
		if escaped {
			word.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' && i+1 < len(runes) && (unicode.IsSpace(runes[i+1]) || runes[i+1] == '\'' || runes[i+1] == '"' || runes[i+1] == '\\') {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			flush()
		default:
			word.WriteRune(r)
		}
	}
	if quote != 0 || escaped {
		return nil, errors.New("caminho incompleto: confira as aspas e tente novamente")
	}
	flush()
	for i, p := range out {
		if strings.HasPrefix(p, "file://") {
			u, err := url.Parse(p)
			if err != nil || (u.Host != "" && u.Host != "localhost") {
				return nil, errors.New("use arquivos desta máquina")
			}
			out[i] = u.Path
		}
	}
	return out, nil
}
func MIME(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		return "text/csv"
	case ".txt":
		return "text/plain"
	case ".pdf":
		return "application/pdf"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".json":
		return "application/json"
	}
	return ""
}
func Safe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}
func Validate(path string) (string, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if MIME(p) == "" {
		return "", errors.New("selecione CSV, TXT, PDF, XLSX ou JSON")
	}
	f, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !f.Mode().IsRegular() || f.Size() == 0 || f.Size() > 50<<20 {
		return "", errors.New("use um arquivo regular de 1 byte a 50 MB")
	}
	return p, nil
}
