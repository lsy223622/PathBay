package pathbay

import (
	"archive/zip"
	"bytes"
	"database/sql"
	_ "embed"
	"fmt"
	"io"
	"time"
)

//go:embed zoneinfo.zip
var zoneData []byte

func zoneBytes(name string) ([]byte, error) {
	if name == "UTC" {
		return nil, nil
	}
	z, err := zip.NewReader(bytes.NewReader(zoneData), int64(len(zoneData)))
	if err != nil {
		return nil, err
	}
	for _, f := range z.File {
		if f.Name == name {
			r, e := f.Open()
			if e != nil {
				return nil, e
			}
			defer r.Close()
			return io.ReadAll(r)
		}
	}
	return nil, fmt.Errorf("unknown timezone %s", name)
}
func zoneFromData(name string, data []byte) (*time.Location, error) {
	if name == "UTC" {
		return time.UTC, nil
	}
	return time.LoadLocationFromTZData(name, data)
}
func (a *App) loadTimezone() error {
	var name string
	var data []byte
	err := a.db.QueryRow("SELECT name,data FROM timezone WHERE id=1").Scan(&name, &data)
	if err == sql.ErrNoRows {
		a.zone.Store(time.UTC)
		return nil
	}
	if err != nil {
		return err
	}
	z, err := zoneFromData(name, data)
	if err != nil {
		return err
	}
	a.zone.Store(z)
	return nil
}
func (a *App) initializeAdmin(username, hash string) error {
	name, data, err := localTimezone()
	if err != nil {
		return err
	}
	z, err := zoneFromData(name, data)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO administrator VALUES(1,?,?)", username, hash); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO timezone VALUES(1,?,?)", name, data); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.zone.Store(z)
	return nil
}
