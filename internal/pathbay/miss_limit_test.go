package pathbay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestMissingRequestLimit(t *testing.T) {
	for _, source := range []string{"unmatched", "directory", "disabled builtin"} {
		t.Run(source, func(t *testing.T) {
			a, err := Open(Options{DataDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			close(a.statsStop)
			<-a.statsDone
			c := a.Config()
			if err := json.Unmarshal([]byte(`{"limit_not_found":true,"detailed_logs":true,"log_unmatched":true}`), &c.Settings); err != nil {
				t.Fatal(err)
			}
			c.Routes = []Route{{ID: "ok", Path: "/ok", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "text", Content: "ok"}}}
			if source == "directory" {
				c.Routes = append(c.Routes, Route{ID: "files", Path: "/", Method: "GET", Match: "prefix", Boundary: true, Enabled: true, Action: Action{Type: "directory", FilePath: t.TempDir()}})
			}
			if err := a.Save(c); err != nil {
				t.Fatal(err)
			}
			request := func(method, path, peer, realIP string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, nil)
				r.RemoteAddr = peer
				r.Header.Set("X-Real-IP", realIP)
				w := httptest.NewRecorder()
				a.ServeHTTP(w, r)
				return w
			}
			for i := 0; i < 20; i++ {
				path := fmt.Sprintf("/missing-%d", i)
				if source == "disabled builtin" {
					path = "/ip"
				}
				if w := request("GET", path, "192.0.2.1:1000", ""); w.Code != http.StatusNotFound {
					t.Fatalf("initial miss %d: %d", i, w.Code)
				}
			}
			for _, method := range []string{"GET", "HEAD"} {
				w := request(method, "/ok", "192.0.2.1:2000", "198.51.100.1")
				if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "300" || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("limited response: %d %v", w.Code, w.Header())
				}
				if method == "HEAD" && w.Body.Len() != 0 {
					t.Fatal("HEAD limit response has a body")
				}
			}
			if len(a.statsQueue) != 20 {
				t.Fatalf("blocked requests entered logs: %d", len(a.statsQueue))
			}
			for key := range a.pending {
				if key.ID == "ok" {
					t.Fatal("blocked request counted as a route hit")
				}
			}
			if w := request("GET", "/ok", "192.0.2.2:1000", ""); w.Code != 200 {
				t.Fatalf("other IP limited: %d", w.Code)
			}
			if w := request("GET", "/admin/", "192.0.2.1:1000", ""); w.Code == http.StatusTooManyRequests {
				t.Fatal("management request limited")
			}
			if w := request("GET", "/ok", "127.0.0.1:1000", "192.0.2.1"); w.Code != http.StatusTooManyRequests {
				t.Fatal("trusted proxy did not identify the limited IP")
			}
			if w := request("GET", "/ok", "127.0.0.1:1000", "192.0.2.3"); w.Code != 200 {
				t.Fatal("trusted proxy clients share a limit")
			}
			if err := json.Unmarshal([]byte(`{"limit_not_found":false}`), &c.Settings); err != nil {
				t.Fatal(err)
			}
			if err := a.Save(c); err != nil {
				t.Fatal(err)
			}
			if w := request("GET", "/ok", "192.0.2.1:1000", ""); w.Code != 200 {
				t.Fatal("disabling the limit did not restore access")
			}
		})
	}
}

func TestMissingRequestLimitWithoutLogs(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c := a.Config()
	c.Routes = []Route{{ID: "ok", Path: "/ok", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "text"}}}
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}
	request := func(method, path string) int {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w.Code
	}
	for i := 0; i < 25; i++ {
		if code := request("GET", "/missing"); code != 404 {
			t.Fatalf("limited by default: %d", code)
		}
	}
	c.Settings.LimitNotFound = true
	if err := a.Save(c); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		if code := request("POST", "/ok"); code != 405 {
			t.Fatalf("method mismatch: %d", code)
		}
	}
	if code := request("GET", "/ok"); code != 200 {
		t.Fatalf("405 triggered limit: %d", code)
	}
	for i := 0; i < 20; i++ {
		if code := request("HEAD", "/missing"); code != 404 {
			t.Fatalf("initial HEAD miss: %d", code)
		}
	}
	if code := request("GET", "/ok"); code != 429 {
		t.Fatalf("missing HEAD requests did not trigger limit without logs: %d", code)
	}
}

func TestMissingRequestLimitExpiry(t *testing.T) {
	var l missLimiter
	at := time.Now()
	for i := 0; i < 19; i++ {
		l.record("192.0.2.1", at)
	}
	at = at.Add(time.Minute)
	l.record("192.0.2.1", at)
	if retry := l.retryAfter("192.0.2.1", at); retry != 0 {
		t.Fatal("previous window triggered a limit", retry)
	}
	for i := 0; i < 18; i++ {
		l.record("192.0.2.1", at)
	}
	if retry := l.retryAfter("192.0.2.1", at); retry != 0 {
		t.Fatal("limited before threshold", retry)
	}
	l.record("192.0.2.1", at)
	if retry := l.retryAfter("192.0.2.1", at); retry != 300 {
		t.Fatal("missing cooldown", retry)
	}
	l.record("192.0.2.1", at.Add(time.Second))
	if retry := l.retryAfter("192.0.2.1", at.Add(5*time.Minute-time.Millisecond)); retry != 1 {
		t.Fatal("cooldown extended or rounded incorrectly", retry)
	}
	at = at.Add(5 * time.Minute)
	if retry := l.retryAfter("192.0.2.1", at); retry != 0 {
		t.Fatal("access not restored at expiry", retry)
	}
	l.record("192.0.2.1", at)
	if retry := l.retryAfter("192.0.2.1", at); retry != 0 {
		t.Fatal("old misses survived cooldown", retry)
	}
}

func TestMissingRequestLimitCapacity(t *testing.T) {
	var l missLimiter
	at := time.Now()
	for i := 0; i < 20; i++ {
		l.record("192.0.2.1", at)
	}
	for i := 0; i < 8192; i++ {
		l.record(fmt.Sprintf("198.18.%d.%d", i/256, i%256), at)
	}
	if len(l.entries) != 8192 {
		t.Fatal("IP tracking capacity not bounded", len(l.entries))
	}
	l.record("192.0.2.2", at.Add(time.Minute))
	if len(l.entries) != 2 || l.retryAfter("192.0.2.1", at.Add(time.Minute)) != 240 {
		t.Fatal("cleanup did not retain active limit and reclaim expired counters")
	}
}

func TestMissingRequestLimitConfiguration(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.ImportConfig([]byte(`{"version":1,"settings":{"limit_not_found":true}}`)); err != nil {
		t.Fatal(err)
	}
	exported, err := a.ExportConfig()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Settings Settings `json:"settings"`
	}
	if err := json.Unmarshal(exported, &doc); err != nil || !doc.Settings.LimitNotFound {
		t.Fatal("limit setting missing from export", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !a.Config().Settings.LimitNotFound {
		t.Fatal("limit setting lost on restart")
	}
}

func TestMissingRequestLimitConcurrent(t *testing.T) {
	var l missLimiter
	at := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Go(func() {
			l.record("192.0.2.1", at)
			l.retryAfter("192.0.2.1", at)
		})
	}
	wg.Wait()
	if retry := l.retryAfter("192.0.2.1", at); retry != 300 {
		t.Fatal("concurrent missing requests did not trigger limit", retry)
	}
}
