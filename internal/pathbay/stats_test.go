package pathbay

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestStatsDayBucketsAndBoundedLogs(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	close(a.statsStop)
	<-a.statsDone
	c := a.Config()
	c.Routes = []Route{{ID: "stats", Path: "/stats", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "text", Content: "ok"}}}
	a.Save(c)
	r := httptest.NewRequest("GET", "/stats?secret=x", nil)
	at := time.Date(2026, 10, 11, 23, 59, 59, 0, time.UTC)
	a.recordVisit(r, 200, "route", "stats", at)
	a.recordVisit(r, 200, "route", "stats", at.Add(2*time.Second))
	if err = a.flushStats(); err != nil {
		t.Fatal(err)
	}
	var days, count int
	a.statsRead.QueryRow("SELECT count(*),sum(hits) FROM daily WHERE hit_id='stats'").Scan(&days, &count)
	if days != 2 || count != 2 {
		t.Fatal(days, count)
	}
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("HEAD", "/stats", nil))
	}
	a.flushStats()
	a.statsRead.QueryRow("SELECT hits FROM summary WHERE hit_id='stats'").Scan(&count)
	if count != 2 {
		t.Fatal("HEAD incremented", count)
	}
	c = a.Config()
	c.Settings.DetailedLogs = true
	c.Settings.Fields.Path = true
	a.Save(c)
	a.statsMu.Lock()
	for i := 0; i < 1100; i++ {
		a.statsQueue = append(a.statsQueue, Visit{})
	}
	a.statsMu.Unlock()
	a.recordVisit(r, 200, "route", "stats", at)
	if a.droppedLogs.Load() == 0 {
		t.Fatal("full queue did not drop")
	}
	a.statsMu.Lock()
	a.statsQueue = nil
	a.statsMu.Unlock()
	a.recordVisit(r, 200, "route", "stats", at)
	if err = a.flushStats(); err != nil {
		t.Fatal(err)
	}
	var path string
	var ua any
	if err = a.statsRead.QueryRow("SELECT path,user_agent FROM logs LIMIT 1").Scan(&path, &ua); err != nil {
		t.Fatal(err)
	}
	if path != "/stats" || ua != nil {
		t.Fatal(path, ua)
	}
	c = a.Config()
	c.Routes = nil
	a.Save(c)
	a.recordVisit(r, 200, "route", "stats", at)
	a.flushStats()
	a.statsRead.QueryRow("SELECT count(*) FROM summary WHERE hit_id='stats'").Scan(&count)
	if count != 0 {
		t.Fatal("deleted stats revived", count)
	}
}

func TestStatsRetryAndBatchAcknowledgement(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	close(a.statsStop)
	<-a.statsDone
	c := a.Config()
	c.Routes = []Route{{ID: "retry", Path: "/retry", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "text"}}}
	a.Save(c)
	r := httptest.NewRequest("GET", "/retry", nil)
	a.recordVisit(r, 200, "route", "retry", time.Now())
	a.statsDB.Exec(`CREATE TRIGGER fail_summary BEFORE INSERT ON summary BEGIN SELECT RAISE(FAIL,'test write failure'); END`)
	if err = a.flushStats(); err == nil {
		t.Fatal("write failure hidden")
	}
	batch := a.batch
	a.statsDB.Exec("DROP TRIGGER fail_summary")
	if err = a.flushStats(); err != nil {
		t.Fatal(err)
	}
	a.batch = batch
	if err = a.flushStats(); err != nil {
		t.Fatal(err)
	}
	var hits int
	a.statsRead.QueryRow("SELECT hits FROM summary WHERE hit_id='retry'").Scan(&hits)
	if hits != 1 {
		t.Fatal("duplicate retry", hits)
	}
}

func TestFailedStatsBatchKeepsSevenDayBound(t *testing.T) {
	a, err := Open(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	close(a.statsStop)
	<-a.statsDone
	c := a.Config()
	c.Routes = []Route{{ID: "bounded", Path: "/bounded", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "text"}}}
	a.Save(c)
	r := httptest.NewRequest("GET", "/bounded", nil)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		a.recordVisit(r, 200, "route", "bounded", at.AddDate(0, 0, i))
	}
	a.statsDB.Exec(`CREATE TRIGGER fail_summary BEFORE INSERT ON summary BEGIN SELECT RAISE(FAIL,'test write failure'); END`)
	if err = a.flushStats(); err == nil {
		t.Fatal("expected failure")
	}
	for i := 7; i < 21; i++ {
		a.recordVisit(r, 200, "route", "bounded", at.AddDate(0, 0, i))
	}
	dates := map[string]bool{}
	for k := range a.pending {
		dates[k.Day] = true
	}
	for k := range a.batch.Counts {
		dates[k.Day] = true
	}
	if len(dates) > 7 {
		t.Fatalf("kept %d uncommitted dates", len(dates))
	}
	a.statsDB.Exec("DROP TRIGGER fail_summary")
}
