package pathbay

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

type Action struct {
	Type          string `json:"type"`
	Content       string `json:"content,omitempty"`
	Format        string `json:"format,omitempty"`
	Target        string `json:"target,omitempty"`
	Status        int    `json:"status,omitempty"`
	PreserveQuery *bool  `json:"preserve_query,omitempty"`
	Source        string `json:"source,omitempty"`
	FilePath      string `json:"file_path,omitempty"`
	FileName      string `json:"file_name,omitempty"`
	FileSize      int64  `json:"file_size,omitempty"`
	Disposition   string `json:"disposition,omitempty"`
	DownloadName  string `json:"download_name,omitempty"`
}
type Route struct {
	body     []byte
	target   *url.URL
	ID       string `json:"id"`
	Path     string `json:"path"`
	Method   string `json:"method"`
	Match    string `json:"match"`
	Boundary bool   `json:"boundary"`
	Enabled  bool   `json:"enabled"`
	Action   Action `json:"action"`
}
type LogFields struct {
	IP        bool `json:"ip"`
	UserAgent bool `json:"user_agent"`
	Referer   bool `json:"referer"`
	Path      bool `json:"path"`
	Method    bool `json:"method"`
	Status    bool `json:"status"`
}
type Settings struct {
	NotFoundHTML  string    `json:"not_found_html"`
	LimitNotFound bool      `json:"limit_not_found"`
	DetailedLogs  bool      `json:"detailed_logs"`
	LogUnmatched  bool      `json:"log_unmatched"`
	Fields        LogFields `json:"fields"`
	RetentionDays int       `json:"retention_days"`
}
type Config struct {
	Routes   []Route         `json:"routes"`
	Builtins map[string]bool `json:"builtins"`
	Settings Settings        `json:"settings"`
}

func cloneConfig(c Config) Config {
	b, _ := json.Marshal(c)
	var out Config
	_ = json.Unmarshal(b, &out)
	return out
}
func newID() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func NormalizePath(p string) (string, error) {
	if strings.ContainsAny(p, "?#") {
		return "", fmt.Errorf("path must not contain query or fragment")
	}
	p, err := url.PathUnescape(p)
	if err != nil {
		return "", err
	}
	return normalizeDecoded(p)
}
func normalizeDecoded(p string) (string, error) {
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path must start with /")
	}
	for _, r := range p {
		if r == '\\' || unicode.IsControl(r) {
			return "", fmt.Errorf("invalid path character")
		}
	}
	trailing := strings.HasSuffix(p, "/")
	p = path.Clean(p)
	if trailing && p != "/" {
		p += "/"
	}
	return p, nil
}
func validateRoute(r *Route) error {
	p, err := normalizeDecoded(r.Path)
	if err != nil {
		return err
	}
	r.Path = p
	if p == "/admin" || strings.HasPrefix(p, "/admin/") || p == "/ip" || p == "/time" || p == "/random" {
		return fmt.Errorf("reserved path")
	}
	if r.Method != "GET" && r.Method != "POST" && r.Method != "ANY" {
		return fmt.Errorf("method must be GET, POST or ANY")
	}
	if r.Match != "exact" && r.Match != "prefix" {
		return fmt.Errorf("match must be exact or prefix")
	}
	if r.Match == "exact" {
		r.Boundary = false
	}
	if len(r.Action.Content) > 1<<20 {
		return fmt.Errorf("content exceeds 1 MiB")
	}
	switch r.Action.Type {
	case "text":
		if r.Action.Format != "" && r.Action.Format != "text" && r.Action.Format != "html" {
			return fmt.Errorf("invalid text format")
		}
		r.body = []byte(r.Action.Content)
	case "json":
		if !json.Valid([]byte(r.Action.Content)) {
			return fmt.Errorf("invalid JSON content")
		}
		r.body = []byte(r.Action.Content)
	case "redirect":
		a := &r.Action
		if a.Status == 0 {
			a.Status = 302
		}
		if a.Status != 301 && a.Status != 302 && a.Status != 307 && a.Status != 308 {
			return fmt.Errorf("invalid redirect status")
		}
		u, e := url.Parse(a.Target)
		if e != nil || u == nil || u.User != nil || strings.HasPrefix(a.Target, "//") || (!(u.Scheme == "http" || u.Scheme == "https") && !strings.HasPrefix(a.Target, "/")) || (u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https") || ((u.Scheme == "http" || u.Scheme == "https") && u.Host == "") {
			return fmt.Errorf("invalid redirect target")
		}
		for _, v := range a.Target {
			if unicode.IsControl(v) {
				return fmt.Errorf("invalid redirect target")
			}
		}
		if _, e = url.ParseQuery(u.RawQuery); e != nil {
			return fmt.Errorf("invalid target query")
		}
		r.target = u
	case "file", "directory":
		if r.Action.FilePath == "" && !(r.Action.Type == "file" && r.Action.Source == "managed") {
			return fmt.Errorf("file path required")
		}
		if r.Action.Type == "file" && r.Action.Source != "local" && r.Action.Source != "managed" {
			return fmt.Errorf("invalid file source")
		}
		if r.Action.Disposition != "" && r.Action.Disposition != "inline" && r.Action.Disposition != "attachment" {
			return fmt.Errorf("invalid disposition")
		}
	default:
		return fmt.Errorf("invalid action type")
	}
	if r.Action.Type != "file" {
		r.Action.Source = ""
		r.Action.FileName = ""
		r.Action.FileSize = 0
		r.Action.Disposition = ""
		r.Action.DownloadName = ""
		if r.Action.Type != "directory" {
			r.Action.FilePath = ""
		}
	} else if r.Action.Source == "local" {
		r.Action.FileName = filepath.Base(r.Action.FilePath)
		r.Action.FileSize = 0
	}
	return nil
}
