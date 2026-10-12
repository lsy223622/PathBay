package pathbay

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestImportAtomicAndStrict(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c := a.Config()
	c.Routes = []Route{{ID: "existing", Path: "/original", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "text", Content: "original"}}}
	a.Save(c)
	for _, data := range []string{`{"version":1,"settings":{"detailed_logs":true},"routes":[{"path":"/original","method":"GET","match":"exact","action":{"type":"text"}}]}`, `{"version":1,"version":1}`, `{"version":1,"unknown":true}`, `{"version":1,"routes":[{"path":"/bad","method":"GET","match":"exact","action":{"type":"json","content":"bad"}}]}`} {
		if err = a.ImportConfig([]byte(data)); err == nil {
			t.Fatal("accepted invalid import", data)
		}
		if a.Config().Settings.DetailedLogs || len(a.Config().Routes) != 1 {
			t.Fatal("partial import")
		}
	}
	if err = a.ImportConfig([]byte(`{"version":1,"routes":[{"id":"existing","path":"/new%252Fpath","method":"GET","match":"exact","enabled":true,"action":{"type":"text","content":"new"}}],"builtins":{"ip":true}}`)); err != nil {
		t.Fatal(err)
	}
	c = a.Config()
	if len(c.Routes) != 2 || c.Routes[1].ID == "existing" || c.Routes[1].Path != "/new%2Fpath" || !c.Builtins["ip"] {
		t.Fatal(c)
	}
	exported, err := a.ExportConfig()
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	json.Unmarshal(exported, &data)
	if data["version"] != float64(1) {
		t.Fatal(data)
	}
}

func TestConfigurationRoundTripEscapedDelimiters(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, token, _ := a.CreateToken("path")
	w := apiTest(a, "POST", "routes", `{"path":"/special%3Fpart%23fragment%25","method":"GET","match":"exact","action":{"type":"text","content":"same path"}}`, "", "", token)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	doc, err := a.ExportConfig()
	if err != nil {
		t.Fatal(err)
	}
	c := a.Config()
	c.Routes = nil
	a.Save(c)
	if err = a.ImportConfig(doc); err != nil {
		t.Fatal("export cannot be imported", err)
	}
	w = httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("GET", "/special%3Fpart%23fragment%25", nil))
	if w.Code != 200 || w.Body.String() != "same path" {
		t.Fatal("round trip changed path", w.Code, w.Body.String())
	}
}
