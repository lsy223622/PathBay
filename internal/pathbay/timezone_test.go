package pathbay

import (
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
