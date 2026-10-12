package pathbay

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFileResponsesAndReplacement(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "index.html"), []byte("<h1>home</h1>"), 0600)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("abcdef"), 0600)
	c := a.Config()
	c.Routes = []Route{{ID: "dir", Path: "/files", Method: "GET", Match: "prefix", Boundary: true, Enabled: true, Action: Action{Type: "directory", FilePath: root}}, {ID: "file", Path: "/download", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "file", Source: "managed"}}}
	if err = a.Save(c); err != nil {
		t.Fatal(err)
	}
	if err = a.Upload("file", "原文.txt", bytes.NewBufferString("first content")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		url, rangeHeader string
		code             int
		body             string
	}{{"/files?q=1", "", 308, ""}, {"/files/", "", 200, "<h1>home</h1>"}, {"/files/a.txt", "bytes=1-3", 206, "bcd"}, {"/files/missing", "", 404, ""}, {"/download", "bytes=0-4", 206, "first"}} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", tc.url, nil)
		r.Header.Set("Range", tc.rangeHeader)
		a.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Errorf("%s %d: %s", tc.url, w.Code, w.Body.String())
		}
		if tc.body != "" && w.Body.String() != tc.body {
			t.Fatal(w.Body.String())
		}
		if tc.code == 308 && w.Header().Get("Location") != "/files/?q=1" {
			t.Fatal(w.Header())
		}
	}
	old := a.Config().Routes[1].Action.FilePath
	if err = a.Upload("file", "new.txt", bytes.NewBufferString("second content")); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("GET", "/download", nil))
	if w.Body.String() != "second content" {
		t.Fatal(w.Body.String())
	}
	a.cleanFiles()
	if _, err = os.Stat(filepath.Join(a.options.DataDir, "files", old)); !os.IsNotExist(err) {
		t.Fatal("retired file retained", err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outside, []byte("secret"), 0600)
	if err = os.Symlink(outside, filepath.Join(root, "escape")); err == nil {
		w = httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("GET", "/files/escape", nil))
		if w.Code != 404 {
			t.Fatal("symlink escaped", w.Code)
		}
	}
	c = a.Config()
	c.Routes = nil
	if err = a.Save(c); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Fatal("deleted external file")
	}
}

func TestConcurrentFileActionChange(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	file := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(file, []byte("file"), 0600)
	route := Route{ID: "changing", Path: "/change", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "file", Source: "local", FilePath: file}}
	c := a.Config()
	c.Routes = []Route{route}
	a.Save(c)
	var group sync.WaitGroup
	failures := make(chan int, 1000)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for j := 0; j < 200; j++ {
				w := httptest.NewRecorder()
				a.ServeHTTP(w, httptest.NewRequest("GET", "/change", nil))
				if w.Code != 200 {
					failures <- w.Code
				}
			}
		}()
	}
	for i := 0; i < 80; i++ {
		c = a.Config()
		c.Routes[0].Action = Action{Type: "text", Content: "text"}
		if i%2 == 0 {
			c.Routes[0] = route
		}
		if err = a.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	group.Wait()
	close(failures)
	for code := range failures {
		t.Fatalf("configuration change caused status %d", code)
	}
}

func TestChangingFileSourceAndAction(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c := a.Config()
	c.Routes = []Route{{ID: "change-file", Path: "/f", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "file", Source: "managed"}}}
	a.Save(c)
	a.Upload("change-file", "old.html", bytes.NewBufferString("old"))
	old := a.Config().Routes[0].Action.FilePath
	_, token, err := a.CreateToken("source-test")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "new.txt")
	os.WriteFile(file, []byte("new"), 0600)
	body, _ := json.Marshal(map[string]any{"action": map[string]any{"source": "local", "file_path": file}})
	w := apiTest(a, "PATCH", "routes/change-file", string(body), "", "", token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("GET", "/f", nil))
	if w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatal("kept old MIME", w.Header())
	}
	a.cleanFiles()
	if _, err = os.Stat(filepath.Join(a.options.DataDir, "files", old)); !os.IsNotExist(err) {
		t.Fatal("source change retained managed file")
	}
	a.Upload("change-file", "again.txt", bytes.NewBufferString("again"))
	old = a.Config().Routes[0].Action.FilePath
	w = apiTest(a, "PATCH", "routes/change-file", `{"action":{"type":"text","content":"text"}}`, "", "", token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a.cleanFiles()
	if _, err = os.Stat(filepath.Join(a.options.DataDir, "files", old)); !os.IsNotExist(err) {
		t.Fatal("action change retained managed file", err)
	}
	body, _ = json.Marshal(map[string]any{"action": map[string]any{"type": "directory", "file_path": filepath.Dir(file)}})
	w = apiTest(a, "PATCH", "routes/change-file", string(body), "", "", token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = apiTest(a, "PATCH", "routes/change-file", `{"action":{"type":"file","source":"managed","file_path":""}}`, "", "", token)
	if w.Code != 200 {
		t.Fatal("cannot choose managed source", w.Code, w.Body.String())
	}
	if err = a.Upload("change-file", "last.txt", bytes.NewBufferString("last")); err != nil {
		t.Fatal(err)
	}
	old = a.Config().Routes[0].Action.FilePath
	body, _ = json.Marshal(map[string]any{"action": map[string]any{"type": "directory", "file_path": filepath.Dir(file)}})
	w = apiTest(a, "PATCH", "routes/change-file", string(body), "", "", token)
	if w.Code != 200 {
		t.Fatal("cannot choose directory", w.Code, w.Body.String())
	}
	a.cleanFiles()
	if _, err = os.Stat(filepath.Join(a.options.DataDir, "files", old)); !os.IsNotExist(err) {
		t.Fatal("directory change retained upload")
	}
}

type pausedFileWriter struct {
	*httptest.ResponseRecorder
	entered, release chan struct{}
	once             sync.Once
}

func (w *pausedFileWriter) Write(b []byte) (int, error) {
	w.once.Do(func() { close(w.entered); <-w.release })
	return w.ResponseRecorder.Write(b)
}
func TestManagedReadSurvivesReplacement(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c := a.Config()
	c.Routes = []Route{{ID: "active", Path: "/active", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "file", Source: "managed"}}}
	a.Save(c)
	original := bytes.Repeat([]byte("a"), 1<<20)
	replacement := bytes.Repeat([]byte("b"), 1<<20)
	a.Upload("active", "original.txt", bytes.NewReader(original))
	old := a.Config().Routes[0].Action.FilePath
	w := &pausedFileWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	finished := make(chan struct{})
	go func() { a.ServeHTTP(w, httptest.NewRequest("GET", "/active", nil)); close(finished) }()
	<-w.entered
	changed := make(chan error, 1)
	go func() { changed <- a.Upload("active", "replacement.txt", bytes.NewReader(replacement)) }()
	select {
	case err = <-changed:
	case <-time.After(2 * time.Second):
		close(w.release)
		<-finished
		t.Fatal("replacement waited for download")
	}
	if err != nil {
		close(w.release)
		<-finished
		t.Fatal(err)
	}
	a.cleanFiles()
	if _, err = os.Stat(filepath.Join(a.options.DataDir, "files", old)); err != nil {
		close(w.release)
		<-finished
		t.Fatal("active file retired early", err)
	}
	next := httptest.NewRecorder()
	a.ServeHTTP(next, httptest.NewRequest("GET", "/active", nil))
	close(w.release)
	<-finished
	if !bytes.Equal(w.Body.Bytes(), original) || !bytes.Equal(next.Body.Bytes(), replacement) {
		t.Fatal("download mixed versions")
	}
	a.cleanFiles()
	if _, err = os.Stat(filepath.Join(a.options.DataDir, "files", old)); !os.IsNotExist(err) {
		t.Fatal("finished file not cleaned")
	}
}

func TestImportReownsRetiringManagedFile(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c := a.Config()
	c.Routes = []Route{{ID: "retiring", Path: "/retiring", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "file", Source: "managed"}}}
	a.Save(c)
	a.Upload("retiring", "saved.txt", bytes.NewBufferString("saved content"))
	export, err := a.ExportConfig()
	if err != nil {
		t.Fatal(err)
	}
	w := &pausedFileWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { a.ServeHTTP(w, httptest.NewRequest("GET", "/retiring", nil)); close(done) }()
	<-w.entered
	c = a.Config()
	c.Routes = nil
	if err = a.Save(c); err != nil {
		close(w.release)
		<-done
		t.Fatal(err)
	}
	if err = a.ImportConfig(export); err != nil {
		close(w.release)
		<-done
		t.Fatal(err)
	}
	close(w.release)
	<-done
	a.cleanFiles()
	next := httptest.NewRecorder()
	a.ServeHTTP(next, httptest.NewRequest("GET", "/retiring", nil))
	if next.Code != 200 || next.Body.String() != "saved content" {
		t.Fatal("retirement deleted imported file", next.Code, next.Body.String())
	}
}

func TestImportProtectsManagedFileUntilCommit(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c := a.Config()
	c.Routes = []Route{{ID: "waiting", Path: "/waiting", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "file", Source: "managed"}}}
	a.Save(c)
	a.Upload("waiting", "file.txt", bytes.NewBufferString("protected"))
	export, _ := a.ExportConfig()
	c = a.Config()
	c.Routes = nil
	a.Save(c)
	tx, err := a.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	waits := a.db.Stats().WaitCount
	done := make(chan error, 1)
	go func() { done <- a.ImportConfig(export) }()
	deadline := time.Now().Add(time.Second)
	for a.db.Stats().WaitCount == waits && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if a.db.Stats().WaitCount == waits {
		t.Fatal("import did not reach commit preparation")
	}
	a.cleanFiles()
	tx.Rollback()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("GET", "/waiting", nil))
	if w.Code != 200 || w.Body.String() != "protected" {
		t.Fatal("cleanup raced import commit", w.Code, w.Body.String())
	}
}
