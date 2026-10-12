package pathbay

import (
	"net/http/httptest"
	"testing"
)

func TestAdminPageAndAssets(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, url := range []string{"/admin/", "/admin/app.js", "/admin/style.css"} {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatal(url, w.Code)
		}
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("GET", "/admin/missing", nil))
	if w.Code != 404 {
		t.Fatal("unknown asset", w.Code)
	}
}
