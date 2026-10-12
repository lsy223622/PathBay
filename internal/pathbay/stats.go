package pathbay

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

type hitKey struct{ Kind, ID, Day string }
type increment struct{ Hits, Last int64 }
type Visit struct {
	ID        int64   `json:"id"`
	Time      int64   `json:"time"`
	Kind      string  `json:"kind"`
	HitID     string  `json:"hit_id"`
	IP        *string `json:"ip"`
	UserAgent *string `json:"user_agent"`
	Referer   *string `json:"referer"`
	Path      *string `json:"path"`
	Method    *string `json:"method"`
	Status    *int    `json:"status"`
}
type statsBatch struct {
	ID        string
	Counts    map[hitKey]increment
	Logs      []Visit
	Uncertain bool
}
type StatsState struct {
	statsDB, statsRead    *sql.DB
	statsMu               sync.Mutex
	flushMu               sync.Mutex
	pending               map[hitKey]increment
	held                  map[hitKey]bool
	statsQueue            []Visit
	batch                 *statsBatch
	droppedLogs, lostHits atomic.Uint64
	statsFailure          atomic.Pointer[string]
	statsWake             chan struct{}
	statsStop             chan struct{}
	statsDone             chan struct{}
	expiryDue             time.Time
}
type statusWriter struct {
	http.ResponseWriter
	code int
	head bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.code != 0 {
		return
	}
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.WriteHeader(200)
	}
	if w.head {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (a *App) initStats() error {
	a.pending = map[hitKey]increment{}
	a.statsWake = make(chan struct{}, 1)
	var err error
	a.statsDB, err = openDB(filepath.Join(a.options.DataDir, "stats.db"), "NORMAL", 1000)
	if err != nil {
		return err
	}
	var version int
	if err = a.statsDB.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return fmt.Errorf("unsupported stats database version")
	}
	_, err = a.statsDB.Exec(`BEGIN; CREATE TABLE IF NOT EXISTS summary(kind TEXT NOT NULL,hit_id TEXT NOT NULL,hits INTEGER NOT NULL,last INTEGER NOT NULL,PRIMARY KEY(kind,hit_id)); CREATE TABLE IF NOT EXISTS daily(kind TEXT NOT NULL,hit_id TEXT NOT NULL,day TEXT NOT NULL,hits INTEGER NOT NULL,PRIMARY KEY(kind,hit_id,day)); CREATE TABLE IF NOT EXISTS logs(id INTEGER PRIMARY KEY,time INTEGER NOT NULL,kind TEXT NOT NULL,hit_id TEXT NOT NULL,ip TEXT,user_agent TEXT,referer TEXT,path TEXT,method TEXT,status INTEGER); CREATE INDEX IF NOT EXISTS logs_time ON logs(time,id); CREATE INDEX IF NOT EXISTS logs_hit ON logs(kind,hit_id,time,id); CREATE TABLE IF NOT EXISTS last_batch(id INTEGER PRIMARY KEY CHECK(id=1),batch TEXT NOT NULL); PRAGMA user_version=1; COMMIT;`)
	if err != nil {
		return err
	}
	a.statsRead, err = openDB(filepath.Join(a.options.DataDir, "stats.db"), "NORMAL", 1000)
	if err != nil {
		return err
	}
	a.statsRead.SetMaxOpenConns(2)
	if err = a.flushStats(); err != nil {
		return err
	}
	a.statsStop = make(chan struct{})
	a.statsDone = make(chan struct{})
	go a.statsLoop()
	return nil
}
func truncated(value string, n int) *string {
	if len(value) > n {
		value = value[:n]
		for !utf8.ValidString(value) && len(value) > 0 {
			value = value[:len(value)-1]
		}
	}
	return &value
}
func (a *App) recordVisit(r *http.Request, status int, kind, id string, at time.Time) {
	settings := a.snapshot.Load().Config.Settings
	a.statsMu.Lock()
	defer a.statsMu.Unlock()
	if kind != "" && r.Method != "HEAD" {
		key := hitKey{kind, id, at.In(a.zone.Load()).Format("2006-01-02")}
		keep := true
		if _, exists := a.pending[key]; !exists {
			keys := map[hitKey]bool{}
			for k := range a.pending {
				if k.Kind == kind && k.ID == id {
					keys[k] = true
				}
			}
			for k := range a.held {
				if k.Kind == kind && k.ID == id {
					keys[k] = true
				}
			}
			if !keys[key] && len(keys) >= 7 {
				old := key
				found := false
				for k := range keys {
					if !a.held[k] && (!found || k.Day < old.Day) {
						old = k
						found = true
					}
				}
				if found {
					a.lostHits.Add(uint64(a.pending[old].Hits))
					delete(a.pending, old)
				} else {
					keep = false
					a.lostHits.Add(1)
				}
			}
		}
		if keep {
			v := a.pending[key]
			v.Hits++
			v.Last = max(v.Last, at.UnixMilli())
			a.pending[key] = v
		}
	}
	if !settings.DetailedLogs || (kind == "" && !settings.LogUnmatched) {
		return
	}
	if len(a.statsQueue) >= 1024 {
		a.droppedLogs.Add(1)
		return
	}
	v := Visit{Time: at.UnixMilli(), Kind: kind, HitID: id}
	f := settings.Fields
	if f.IP {
		ip := a.clientIP(r)
		v.IP = &ip
	}
	if f.UserAgent {
		v.UserAgent = truncated(r.UserAgent(), 512)
	}
	if f.Referer {
		v.Referer = truncated(r.Referer(), 1024)
	}
	if f.Path {
		v.Path = truncated(r.URL.Path, 2048)
	}
	if f.Method {
		v.Method = truncated(r.Method, 32)
	}
	if f.Status {
		v.Status = &status
	}
	a.statsQueue = append(a.statsQueue, v)
	if len(a.statsQueue) >= 256 {
		select {
		case a.statsWake <- struct{}{}:
		default:
		}
	}
}
func (a *App) flushStats() error { return a.flushStatsContext(context.Background(), true) }
func (a *App) flushStatsContext(ctx context.Context, summary bool) (result error) {
	a.flushMu.Lock()
	defer a.flushMu.Unlock()
	defer func() {
		if result != nil && a.batch != nil && !a.batch.Uncertain {
			a.statsMu.Lock()
			for key, v := range a.batch.Counts {
				p := a.pending[key]
				p.Hits += v.Hits
				p.Last = max(p.Last, v.Last)
				a.pending[key] = p
			}
			a.batch.Counts = nil
			a.held = nil
			a.trimDays()
			a.statsMu.Unlock()
		}
	}()
	if a.batch == nil {
		a.statsMu.Lock()
		counts := map[hitKey]increment{}
		if summary {
			counts = a.pending
			a.pending = map[hitKey]increment{}
		}
		a.held = map[hitKey]bool{}
		for key := range counts {
			a.held[key] = true
		}
		n := min(256, len(a.statsQueue))
		entries := append([]Visit(nil), a.statsQueue[:n]...)
		a.statsQueue = append([]Visit(nil), a.statsQueue[n:]...)
		a.statsMu.Unlock()
		a.batch = &statsBatch{ID: newID(), Counts: counts, Logs: entries}
	}
	batch := a.batch
	if batch.Counts == nil && !batch.Uncertain && summary {
		a.statsMu.Lock()
		batch.Counts = a.pending
		a.pending = map[hitKey]increment{}
		a.held = map[hitKey]bool{}
		for key := range batch.Counts {
			a.held[key] = true
		}
		a.statsMu.Unlock()
	}
	var last string
	err := a.statsDB.QueryRowContext(ctx, "SELECT batch FROM last_batch WHERE id=1").Scan(&last)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if last == batch.ID {
		a.batch = nil
		a.statsMu.Lock()
		a.held = nil
		a.statsMu.Unlock()
		return nil
	}
	tx, err := a.statsDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	s := a.snapshot.Load()
	valid := func(kind, id string) bool { return kind != "route" || s.ids[id] }
	rows, err := tx.QueryContext(ctx, "SELECT hit_id FROM summary WHERE kind='route' UNION SELECT hit_id FROM daily WHERE kind='route' UNION SELECT hit_id FROM logs WHERE kind='route'")
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !s.ids[id] {
			stale = append(stale, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range stale {
		for _, table := range []string{"summary", "daily", "logs"} {
			if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE kind='route' AND hit_id=?", id); err != nil {
				return err
			}
		}
	}
	for key, v := range batch.Counts {
		if !valid(key.Kind, key.ID) {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO summary VALUES(?,?,?,?) ON CONFLICT(kind,hit_id) DO UPDATE SET hits=hits+excluded.hits,last=max(last,excluded.last)`, key.Kind, key.ID, v.Hits, v.Last); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO daily VALUES(?,?,?,?) ON CONFLICT(kind,hit_id,day) DO UPDATE SET hits=hits+excluded.hits`, key.Kind, key.ID, key.Day, v.Hits); err != nil {
			return err
		}
	}
	for _, v := range batch.Logs {
		if !valid(v.Kind, v.HitID) {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO logs(time,kind,hit_id,ip,user_agent,referer,path,method,status) VALUES(?,?,?,?,?,?,?,?,?)`, v.Time, v.Kind, v.HitID, v.IP, v.UserAgent, v.Referer, v.Path, v.Method, v.Status); err != nil {
			return err
		}
	}
	if !time.Now().Before(a.expiryDue) {
		days := s.Config.Settings.RetentionDays
		if days > 0 {
			res, e := tx.ExecContext(ctx, "DELETE FROM logs WHERE id IN (SELECT id FROM logs WHERE time<? LIMIT 1000)", time.Now().Add(-time.Duration(days)*24*time.Hour).UnixMilli())
			if e != nil {
				return e
			}
			n, _ := res.RowsAffected()
			if n < 1000 {
				a.expiryDue = time.Now().Add(time.Hour)
			}
		} else {
			a.expiryDue = time.Now().Add(time.Hour)
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO last_batch VALUES(1,?) ON CONFLICT(id) DO UPDATE SET batch=excluded.batch", batch.ID); err != nil {
		return err
	}
	batch.Uncertain = true
	if err = tx.Commit(); err != nil {
		return err
	}
	a.batch = nil
	a.statsMu.Lock()
	a.held = nil
	a.statsMu.Unlock()
	return nil
}
func (a *App) trimDays() {
	groups := map[string][]hitKey{}
	for key := range a.pending {
		group := key.Kind + ":" + key.ID
		groups[group] = append(groups[group], key)
	}
	for _, keys := range groups {
		sort.Slice(keys, func(i, j int) bool { return keys[i].Day < keys[j].Day })
		for len(keys) > 7 {
			old := keys[0]
			a.lostHits.Add(uint64(a.pending[old].Hits))
			delete(a.pending, old)
			keys = keys[1:]
		}
	}
}
func (a *App) statsLoop() {
	defer close(a.statsDone)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	nextSummary := time.Now().Add(5 * time.Second)
	nextAttempt := time.Time{}
	backoff := time.Second
	for {
		select {
		case <-a.statsStop:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			for {
				err := a.flushStatsContext(ctx, true)
				if err != nil {
					log.Printf("statistics shutdown: %v", err)
					return
				}
				a.statsMu.Lock()
				empty := len(a.statsQueue) == 0 && len(a.pending) == 0
				a.statsMu.Unlock()
				if empty {
					return
				}
			}
		case <-tick.C:
		case <-a.statsWake:
		}
		now := time.Now()
		if now.Before(nextAttempt) {
			continue
		}
		summary := !now.Before(nextSummary)
		err := a.flushStatsContext(context.Background(), summary)
		a.cleanFiles()
		if err != nil {
			message := err.Error()
			a.statsFailure.Store(&message)
			nextAttempt = now.Add(backoff)
			backoff = min(30*time.Second, backoff*2)
		} else {
			a.statsFailure.Store(nil)
			nextAttempt = time.Time{}
			backoff = time.Second
			if summary {
				nextSummary = now.Add(5 * time.Second)
			}
		}
	}
}
