package pathbay

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
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
