package pathbay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type configDocument struct {
	Version  int             `json:"version"`
	Routes   []Route         `json:"routes"`
	Builtins map[string]bool `json:"builtins,omitempty"`
	Settings json.RawMessage `json:"settings,omitempty"`
}

func checkJSONKeys(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	var value func() error
	value = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			keys := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return e
				}
				name := key.(string)
				if keys[name] {
					return fmt.Errorf("duplicate JSON key %q", name)
				}
				keys[name] = true
				if e = value(); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := value(); e != nil {
					return e
				}
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}
func (a *App) ExportConfig() ([]byte, error) {
	c := a.Config()
	for i := range c.Routes {
		c.Routes[i].Path = strings.ReplaceAll(c.Routes[i].Path, "%", "%25")
	}
	settings, _ := json.Marshal(c.Settings)
	return json.MarshalIndent(configDocument{Version: 1, Routes: c.Routes, Builtins: c.Builtins, Settings: settings}, "", "  ")
}
func (a *App) ImportConfig(b []byte) error {
	if len(b) > 16<<20 {
		return fmt.Errorf("import exceeds 16 MiB")
	}
	if err := checkJSONKeys(b); err != nil {
		return err
	}
	var doc configDocument
	if err := strictDecode(b, &doc); err != nil {
		return err
	}
	if doc.Version != 1 {
		return fmt.Errorf("unsupported config version")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.Config()
	for _, route := range doc.Routes {
		route.ID = newID()
		p, err := NormalizePath(route.Path)
		if err != nil {
			return err
		}
		route.Path = p
		if err = validateRoute(&route); err != nil {
			return err
		}
		if route.Action.Source == "managed" {
			v := route.Action
			if v.FilePath == "" || filepath.Base(v.FilePath) != v.FilePath || strings.ContainsAny(v.FilePath, "/\\") || v.FilePath == "." || v.FilePath == ".." {
				return fmt.Errorf("invalid managed reference")
			}
			root, err := os.OpenRoot(filepath.Join(a.options.DataDir, "files"))
			if err != nil {
				return err
			}
			f, err := root.Open(v.FilePath)
			root.Close()
			if err != nil {
				return err
			}
			info, err := f.Stat()
			f.Close()
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("invalid managed file")
			}
			route.Action.FileSize = info.Size()
		}
		c.Routes = append(c.Routes, route)
	}
	for key, value := range doc.Builtins {
		c.Builtins[key] = value
	}
	if len(doc.Settings) > 0 {
		if err := strictDecode(doc.Settings, &c.Settings); err != nil {
			return err
		}
	}
	return a.saveLocked(c)
}
