package pathbay

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCustomNotFoundResponses(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	request := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	if w := request("GET", "/missing"); w.Code != 404 || w.Body.String() != "Not Found\n" {
		t.Fatal("default 404 changed", w.Code, w.Body.String())
	}
	const html = "<!doctype html><html lang=\"zh\"><body><h1>页面不存在</h1></body></html>"
	c := a.Config()
	settings, _ := json.Marshal(map[string]string{"not_found_html": html})
	if err := json.Unmarshal(settings, &c.Settings); err != nil {
		t.Fatal(err)
	}
	c.Routes = []Route{
		{ID: "ok", Path: "/ok", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "text", Content: "route body"}},
		{ID: "directory", Path: "/files", Method: "GET", Match: "prefix", Boundary: true, Enabled: true, Action: Action{Type: "directory", FilePath: t.TempDir()}},
		{ID: "file", Path: "/download", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "file", Source: "managed"}},
	}
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/missing", "/files/missing", "/files/", "/download", "/ip", "/time", "/random"} {
		for _, method := range []string{"GET", "HEAD"} {
			w := request(method, path)
			if w.Code != 404 || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
				t.Errorf("%s %s: %d %v", method, path, w.Code, w.Header())
			}
			want := html
			if method == "HEAD" {
				want = ""
			}
			if w.Body.String() != want {
				t.Errorf("%s %s body: %q", method, path, w.Body.String())
			}
		}
	}
	if w := request("GET", "/ok"); w.Code != 200 || w.Body.String() != "route body" {
		t.Fatal("normal route overridden", w.Code, w.Body.String())
	}
	if w := request("POST", "/ok"); w.Code != 405 || strings.Contains(w.Body.String(), html) {
		t.Fatal("method error overridden", w.Code, w.Body.String())
	}
	if w := request("GET", "/admin/missing"); w.Code != 404 || w.Body.String() == html {
		t.Fatal("management page overridden", w.Code, w.Body.String())
	}
	_, token, err := a.CreateToken("404-test")
	if err != nil {
		t.Fatal(err)
	}
	if w := apiTest(a, "GET", "missing", "", "", "", token); w.Code != 404 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") || !json.Valid(w.Body.Bytes()) {
		t.Fatal("management API overridden", w.Code, w.Body.String())
	}
	settings, _ = json.Marshal(map[string]string{"not_found_html": ""})
	if err := json.Unmarshal(settings, &c.Settings); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", "/missing"); w.Code != 404 || w.Body.String() != "Not Found\n" {
		t.Fatal("clearing custom HTML did not restore default", w.Code, w.Body.String())
	}
}

func TestCustomNotFoundSettings(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, token, err := a.CreateToken("404-settings")
	if err != nil {
		t.Fatal(err)
	}
	html := "<!doctype html><html><body>" + strings.Repeat("page content ", 3000) + "</body></html>"
	body, _ := json.Marshal(map[string]string{"not_found_html": html})
	w := apiTest(a, "PATCH", "settings", string(body), "", "", token)
	if w.Code != 200 {
		t.Fatal("saving a complete page failed", w.Code, w.Body.String())
	}
	if a.Config().Settings.RetentionDays != 30 {
		t.Fatal("partial settings update changed retention")
	}
	body, _ = json.Marshal(map[string]string{"not_found_html": strings.Repeat("x", (1<<20)+1)})
	w = apiTest(a, "PATCH", "settings", string(body), "", "", token)
	if w.Code != 400 || a.Config().Settings.NotFoundHTML != html {
		t.Fatal("oversized page accepted or changed active configuration", w.Code)
	}
	exported, err := a.ExportConfig()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Settings Settings `json:"settings"`
	}
	if err := json.Unmarshal(exported, &doc); err != nil || doc.Settings.NotFoundHTML != html {
		t.Fatal("404 HTML missing from export", err)
	}
	if err := a.ImportConfig([]byte(`{"version":1,"settings":{"not_found_html":""}}`)); err != nil {
		t.Fatal(err)
	}
	if a.Config().Settings.NotFoundHTML != "" {
		t.Fatal("import did not clear page")
	}
	if err := a.ImportConfig(exported); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	w = httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("GET", "/missing", nil))
	if w.Code != 404 || w.Body.String() != html {
		t.Fatal("imported page lost on restart", w.Code)
	}
}

func TestCustomNotFoundCountsTowardsLimit(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	const html = "<h1>Not found</h1>"
	c := a.Config()
	c.Settings.NotFoundHTML = html
	c.Settings.LimitNotFound = true
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("GET", "/missing", nil))
		if w.Code != 404 || w.Body.String() != html {
			t.Fatal("initial custom 404 response", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("GET", "/missing", nil))
	if w.Code != 429 || w.Body.String() == html {
		t.Fatal("custom 404 bypassed rate limit", w.Code, w.Body.String())
	}
}
