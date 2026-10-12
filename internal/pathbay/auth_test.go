package pathbay

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func apiTest(a *App, method, path, body string, cookie, csrf, bearer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/admin/api/v1/"+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}
func TestAuthenticationAndTokenRevocation(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	w := apiTest(a, "POST", "setup", `{"username":"admin","password":"a sufficiently long password"}`, "", "", "")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Set-Cookie"), "Secure") {
		t.Fatal("missing secure cookie")
	}
	cookie := strings.Split(w.Header().Get("Set-Cookie"), ";")[0]
	var session struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(w.Body.Bytes(), &session)
	if apiTest(a, "POST", "setup", `{"username":"other","password":"long enough password"}`, "", "", "").Code != 409 {
		t.Fatal("initialized twice")
	}
	if apiTest(a, "GET", "routes", "", "", "", "").Code != 401 {
		t.Fatal("unauthenticated accepted")
	}
	if apiTest(a, "PATCH", "builtins", `{"ip":true}`, cookie, "", "").Code != 403 {
		t.Fatal("missing csrf accepted")
	}
	if apiTest(a, "PATCH", "builtins", `{"ip":true}`, cookie, session.CSRF, "").Code != 200 {
		t.Fatal("csrf rejected")
	}
	w = apiTest(a, "POST", "tokens", `{"name":"automation"}`, cookie, session.CSRF, "")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var token struct{ ID, Token string }
	json.Unmarshal(w.Body.Bytes(), &token)
	if apiTest(a, "PATCH", "builtins", `{"time":true}`, "", "", token.Token).Code != 200 {
		t.Fatal("bearer rejected")
	}
	if apiTest(a, "GET", "routes", "", cookie, "", "bad").Code != 401 {
		t.Fatal("bad bearer fell back to cookie")
	}
	if apiTest(a, "DELETE", "tokens/"+token.ID, "", cookie, session.CSRF, "").Code != 204 {
		t.Fatal("revoke failed")
	}
	if apiTest(a, "GET", "routes", "", "", "", token.Token).Code != 401 {
		t.Fatal("revoked token accepted")
	}
}

func TestManagementRequestDeadlineBoundsLockWait(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, token, err := a.CreateToken("deadline")
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- apiTest(a, "GET", "routes", "", "", "", token) }()
	select {
	case w := <-done:
		if w.Code != 503 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result map[string]any
		if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || result["error"] == nil {
			t.Fatal("timeout is not an API error", err)
		}
	case <-time.After(31 * time.Second):
		t.Fatal("management request exceeded 30-second deadline while waiting for configuration lock")
	}
}
