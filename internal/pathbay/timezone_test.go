package pathbay

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSavedTimezoneSurvivesEnvironmentChange(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TZ", "America/New_York")
	a, err := Open(Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := a.hashPassword("timezone test password")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.initializeAdmin("admin", hash); err != nil {
		t.Fatal(err)
	}
	a.Close()
	t.Setenv("TZ", "UTC")
	a, err = Open(Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	summer := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC).In(a.zone.Load())
	winter := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).In(a.zone.Load())
	if summer.Hour() != 8 || winter.Hour() != 7 {
		t.Fatal(summer, winter)
	}
}

func TestStatisticsDisplayUsesSavedRules(t *testing.T) {
	t.Setenv("TZ", "America/New_York")
	dir := t.TempDir()
	a, err := Open(Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := a.hashPassword("saved timezone password")
	if err = a.initializeAdmin("admin", hash); err != nil {
		t.Fatal(err)
	}
	c := a.Config()
	c.Routes = []Route{{ID: "zone", Path: "/zone", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "text"}}}
	c.Settings.DetailedLogs = true
	c.Settings.RetentionDays = 0
	a.Save(c)
	a.recordVisit(httptest.NewRequest("GET", "/zone", nil), 200, "route", "zone", time.Date(2026, 7, 1, 1, 0, 0, 0, time.UTC))
	a.flushStats()
	_, token, _ := a.CreateToken("time-display")
	a.Close()
	t.Setenv("TZ", "UTC")
	a, err = Open(Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var summary struct {
		Items []struct {
			Display string `json:"last_display"`
		}
	}
	w := apiTest(a, "GET", "stats/summary", "", "", "", token)
	if err = json.Unmarshal(w.Body.Bytes(), &summary); err != nil || len(summary.Items) != 1 || summary.Items[0].Display != "2026-06-30 21:00:00 -0400" {
		t.Fatal("summary does not use saved rules", w.Body.String())
	}
	var logs struct {
		Items []struct {
			Display string `json:"time_display"`
		}
	}
	w = apiTest(a, "GET", "stats/logs", "", "", "", token)
	if err = json.Unmarshal(w.Body.Bytes(), &logs); err != nil || len(logs.Items) != 1 || logs.Items[0].Display != "2026-06-30 21:00:00 -0400" {
		t.Fatal("logs do not use saved rules", w.Body.String())
	}
}
