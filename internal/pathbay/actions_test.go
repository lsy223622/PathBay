package pathbay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestActionsAndBuiltin(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c := a.Config()
	c.Routes = []Route{{ID: "r", Path: "/go", Method: "ANY", Match: "exact", Enabled: true, Action: Action{Type: "redirect", Target: "https://example.com/page?lang=zh&theme=dark#part"}}, {ID: "j", Path: "/json", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "json", Content: `{"x":1}`}}, {ID: "h", Path: "/html", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "text", Format: "html", Content: "<b>test</b>"}}}
	c.Builtins["random"] = true
	c.Builtins["time"] = true
	c.Builtins["ip"] = true
	if err = a.Save(c); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, url string
		code        int
		body        string
	}{
		{"GET", "/go?lang=en&lang=fr&utm=x", 302, ""}, {"GET", "/go?x=%ZZ", 400, ""}, {"GET", "/json", 200, `{"x":1}`}, {"HEAD", "/html", 200, ""}, {"POST", "/random", 405, ""}, {"GET", "/random?length=0", 400, ""}, {"GET", "/random?template=uuid&length=bad", 200, ""}, {"GET", "/time?tz=UTC&format=unix", 200, ""}, {"GET", "/time?tz=bad", 400, ""}, {"GET", "/time?format=unix&format=unix_ms", 400, ""},
	} {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest(tc.method, tc.url, nil))
		if w.Code != tc.code {
			t.Errorf("%s: %d %s", tc.url, w.Code, w.Body.String())
		}
		if tc.body != "" && w.Body.String() != tc.body {
			t.Fatal(w.Body.String())
		}
		if strings.HasPrefix(tc.url, "/go?lang") && w.Header().Get("Location") != "https://example.com/page?lang=en&lang=fr&theme=dark&utm=x#part" {
			t.Fatal(w.Header())
		}
		if strings.Contains(tc.url, "uuid") && len(strings.TrimSpace(w.Body.String())) != 36 {
			t.Fatal(w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/ip", nil)
	r.RemoteAddr = "192.0.2.1:4321"
	r.Header.Set("X-Real-IP", "1.2.3.4")
	a.ServeHTTP(w, r)
	if strings.TrimSpace(w.Body.String()) != "192.0.2.1" {
		t.Fatal(w.Body.String())
	}
	c = a.Config()
	c.Builtins["ip"] = false
	c.Routes = append(c.Routes, Route{ID: "catch", Path: "/", Method: "ANY", Match: "prefix", Enabled: true, Action: Action{Type: "text", Content: "catch"}})
	a.Save(c)
	w = httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("GET", "/ip", nil))
	if w.Code != 404 {
		t.Fatal("disabled builtin fell through")
	}
	a.db.Close()
	w = httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("GET", "/json", nil))
	if w.Code != 200 {
		t.Fatal("public response used db")
	}
}

type responseSink struct{ header http.Header }

func (w *responseSink) Header() http.Header         { return w.header }
func (w *responseSink) WriteHeader(int)             {}
func (w *responseSink) Write(b []byte) (int, error) { return len(b), nil }
func TestFixedResponseDoesNotCopyContentPerRequest(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c := a.Config()
	c.Routes = []Route{{ID: "large-text", Path: "/text", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "text", Content: strings.Repeat("x", 1<<20)}}}
	a.Save(c)
	r := httptest.NewRequest("GET", "/text", nil)
	w := &responseSink{header: make(http.Header)}
	result := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			a.ServeHTTP(w, r)
		}
	})
	if result.AllocedBytesPerOp() > 64<<10 {
		t.Fatalf("fixed response allocates %d bytes/request", result.AllocedBytesPerOp())
	}
}
