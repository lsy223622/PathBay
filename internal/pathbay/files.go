package pathbay

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (a *App) fileResponse(w http.ResponseWriter, r *http.Request) (selected *Route) {
	a.fileMu.RLock()
	s := a.snapshot.Load()
	route, code, allow := s.Match(r.Method, r.URL.Path)
	selected = route
	if route == nil {
		a.fileMu.RUnlock()
		if allow != "" {
			w.Header().Set("Allow", allow)
		}
		http.Error(w, http.StatusText(code), code)
		return
	}
	if route.Action.Type != "file" && route.Action.Type != "directory" {
		a.fileMu.RUnlock()
		a.respond(w, r, route)
		return
	}
	v := route.Action
	var f *os.File
	var err error
	name := v.FileName
	managed := ""
	if v.Type == "directory" {
		var root *os.Root
		root, err = os.OpenRoot(v.FilePath)
		if err == nil {
			relative := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, route.Path), "/")
			if relative == "" {
				relative = "."
			}
			f, err = root.Open(relative)
			if err == nil {
				var info os.FileInfo
				info, err = f.Stat()
				if err == nil && info.IsDir() {
					f.Close()
					f = nil
					if !strings.HasSuffix(r.URL.Path, "/") {
						root.Close()
						a.fileMu.RUnlock()
						location := r.URL.EscapedPath() + "/"
						if r.URL.RawQuery != "" {
							location += "?" + r.URL.RawQuery
						}
						w.Header().Set("Location", location)
						w.WriteHeader(308)
						return
					}
					f, err = root.Open(filepath.Join(relative, "index.html"))
				}
			}
			root.Close()
		}
		if f != nil {
			name = f.Name()
		}
	} else if v.Source == "managed" {
		managed = v.FilePath
		if managed == "" {
			err = os.ErrNotExist
		} else {
			f, err = os.Open(filepath.Join(a.options.DataDir, "files", managed))
		}
	} else {
		f, err = os.Open(v.FilePath)
		if name == "" {
			name = filepath.Base(v.FilePath)
		}
	}
	if err == nil {
		var info os.FileInfo
		info, err = f.Stat()
		if err == nil && !info.Mode().IsRegular() {
			err = os.ErrNotExist
		}
	}
	if err != nil {
		if f != nil {
			f.Close()
		}
		a.fileMu.RUnlock()
		code := 500
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "escapes") {
			code = 404
		} else if errors.Is(err, os.ErrPermission) {
			code = 403
		}
		http.Error(w, http.StatusText(code), code)
		return
	}
	if managed != "" {
		a.readMu.Lock()
		a.readers[managed]++
		a.readMu.Unlock()
	}
	a.fileMu.RUnlock()
	defer func() {
		f.Close()
		if managed != "" {
			a.readMu.Lock()
			a.readers[managed]--
			a.readMu.Unlock()
		}
	}()
	info, _ := f.Stat()
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if v.Type == "file" {
		disposition := v.Disposition
		if disposition == "" {
			disposition = "inline"
		}
		download := v.DownloadName
		if download == "" {
			download = name
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": download}))
	}
	http.ServeContent(w, r, name, info.ModTime(), f)
	return
}
func (a *App) Upload(id, name string, body io.Reader) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.Config()
	index := -1
	for i, r := range c.Routes {
		if r.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return os.ErrNotExist
	}
	if c.Routes[index].Action.Type != "file" {
		return fmt.Errorf("route must use file action")
	}
	dir := filepath.Join(a.options.DataDir, "files")
	f, err := os.CreateTemp(dir, ".upload-")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	size, err := io.Copy(f, io.LimitReader(body, a.options.UploadLimit+1))
	if err == nil && size > a.options.UploadLimit {
		err = fmt.Errorf("upload exceeds limit")
	}
	if err == nil {
		err = f.Sync()
	}
	e := f.Close()
	if err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	storage := newID()
	target := filepath.Join(dir, storage)
	if err = os.Rename(temp, target); err != nil {
		return err
	}
	v := &c.Routes[index].Action
	v.Source = "managed"
	v.FilePath = storage
	v.FileName = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	v.FileSize = size
	if err = a.saveLocked(c); err != nil {
		os.Remove(target)
		return err
	}
	a.cleanFiles()
	return nil
}
func managedReferences(s *Snapshot) map[string]bool {
	out := map[string]bool{}
	if s != nil {
		for _, r := range s.Config.Routes {
			if r.Action.Source == "managed" && r.Action.FilePath != "" {
				out[r.Action.FilePath] = true
			}
		}
	}
	return out
}
func (a *App) publish(s *Snapshot) {
	a.fileMu.Lock()
	old := managedReferences(a.snapshot.Load())
	next := managedReferences(s)
	a.snapshot.Store(s)
	a.readMu.Lock()
	for id := range old {
		if !next[id] {
			a.retired[id] = true
		}
	}
	a.readMu.Unlock()
	a.fileMu.Unlock()
}
func (a *App) cleanFiles() {
	a.readMu.Lock()
	defer a.readMu.Unlock()
	for id := range a.retired {
		if a.readers[id] != 0 {
			continue
		}
		err := os.Remove(filepath.Join(a.options.DataDir, "files", id))
		if err == nil || os.IsNotExist(err) {
			delete(a.retired, id)
			delete(a.readers, id)
		}
	}
}
func (a *App) recoverFiles() error {
	refs := managedReferences(a.snapshot.Load())
	entries, err := os.ReadDir(filepath.Join(a.options.DataDir, "files"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !refs[entry.Name()] && !entry.IsDir() {
			a.retired[entry.Name()] = true
		}
	}
	a.cleanFiles()
	return nil
}
func validateFiles(c Config) error {
	owned := map[string]bool{}
	for _, r := range c.Routes {
		v := r.Action
		if v.Type != "file" && v.Type != "directory" {
			continue
		}
		if v.Source == "managed" {
			if v.FilePath != "" {
				if filepath.Base(v.FilePath) != v.FilePath || strings.ContainsAny(v.FilePath, "/\\") || v.FilePath == "." || v.FilePath == ".." || owned[v.FilePath] {
					return fmt.Errorf("invalid or already owned managed file")
				}
				owned[v.FilePath] = true
			}
			continue
		}
		if !filepath.IsAbs(v.FilePath) {
			return fmt.Errorf("file path must be absolute")
		}
		f, err := os.Open(v.FilePath)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		f.Close()
		if err != nil {
			return err
		}
		if (v.Type == "directory" && !info.IsDir()) || (v.Type == "file" && !info.Mode().IsRegular()) {
			return fmt.Errorf("wrong file type")
		}
	}
	return nil
}
