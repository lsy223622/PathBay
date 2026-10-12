package pathbay

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizePath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/foo//a/../bar/", "/foo/bar/"}, {"/foo%2Fbar", "/foo/bar"}, {"/foo%252Fbar", "/foo%2Fbar"}, {"/../../foo", "/foo"}, {"/中文", "/中文"},
	} {
		got, err := NormalizePath(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%q => %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"/foo%5cbar", "/foo%00bar", "/foo%zz", "foo", "/foo?x=1", "/foo#bar"} {
		if _, err := NormalizePath(in); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestPathBeforeMethod(t *testing.T) {
	s, err := BuildSnapshot(Config{Routes: []Route{
		{ID: "root", Path: "/", Method: "GET", Match: "prefix", Enabled: true, Action: Action{Type: "text", Content: "root"}},
		{ID: "exact", Path: "/foo", Method: "ANY", Match: "exact", Enabled: true, Action: Action{Type: "text", Content: "exact"}},
		{ID: "get", Path: "/same", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "text", Content: "get"}},
		{ID: "any", Path: "/same", Method: "ANY", Match: "exact", Enabled: true, Action: Action{Type: "text", Content: "any"}},
		{ID: "boundary", Path: "/api", Method: "POST", Match: "prefix", Boundary: true, Enabled: true, Action: Action{Type: "text"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, id string
		code             int
	}{
		{"GET", "/foo", "exact", 200}, {"HEAD", "/same", "get", 200}, {"POST", "/same", "any", 200}, {"POST", "/api/x", "boundary", 200}, {"POST", "/api2", "", 405}, {"TRACE", "/same", "", 405}, {"CUSTOM", "/same", "", 501},
	} {
		r, code, _ := s.Match(tc.method, tc.path)
		id := ""
		if r != nil {
			id = r.ID
		}
		if id != tc.id || code != tc.code {
			t.Errorf("%s %s => %s %d", tc.method, tc.path, id, code)
		}
	}
	_, _, allow := s.Match("POST", "/else")
	if allow != "GET, HEAD" {
		t.Fatal(allow)
	}
}

func TestPersistenceAndFailedSave(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	c := a.Config()
	c.Routes = append(c.Routes, Route{ID: "persist", Path: "/x", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "text", Content: "saved"}})
	if err = a.Save(c); err != nil {
		t.Fatal(err)
	}
	bad := a.Config()
	bad.Routes = append(bad.Routes, bad.Routes[0])
	if a.Save(bad) == nil {
		t.Fatal("duplicate accepted")
	}
	a.db.Close()
	c.Routes[0].Action.Content = "failed"
	if a.Save(c) == nil {
		t.Fatal("closed DB accepted")
	}
	if a.snapshot.Load().Config.Routes[0].Action.Content != "saved" {
		t.Fatal("failed commit published")
	}
	a.Close()
	a, err = Open(Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Config().Routes[0].Action.Content != "saved" {
		t.Fatal("lost persisted route")
	}
	if _, err = Open(Options{DataDir: dir}); err == nil {
		t.Fatal("second process lock accepted")
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest(http.MethodHead, "/x", nil))
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestHeadErrorsHaveNoBody(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, path := range []string{"/missing", "/ip", "/bad%5Cpath"} {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("HEAD", path, nil))
		if w.Body.Len() != 0 {
			t.Errorf("%s HEAD body: %q", path, w.Body.String())
		}
	}
}
