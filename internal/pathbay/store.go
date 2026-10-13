package pathbay

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/gofrs/flock"
	_ "github.com/mattn/go-sqlite3"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

type Options struct {
	DataDir        string
	TrustedProxies []netip.Prefix
	UploadLimit    int64
}
type App struct {
	StatsState
	misses    missLimiter
	db        *sql.DB
	lock      *flock.Flock
	options   Options
	snapshot  atomic.Pointer[Snapshot]
	mu        sync.Mutex
	closeOnce sync.Once
	fileMu    sync.RWMutex
	readMu    sync.Mutex
	readers   map[string]int
	retired   map[string]bool
	authMu    sync.Mutex
	sessions  map[[32]byte]session
	tokens    atomic.Pointer[map[string]Token]
	hashSlot  chan struct{}
	origin    *http.CrossOriginProtection
	zone      atomic.Pointer[time.Location]
}

func openDB(p, synchronous string, busy int) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", filepath.ToSlash(p)+fmt.Sprintf("?_journal_mode=WAL&_synchronous=%s&_busy_timeout=%d&_foreign_keys=on&_cache_size=-2048", synchronous, busy))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func Open(o Options) (*App, error) {
	if o.DataDir == "" {
		o.DataDir = "data"
	}
	abs, err := filepath.Abs(o.DataDir)
	if err != nil {
		return nil, err
	}
	o.DataDir = abs
	if o.UploadLimit == 0 {
		o.UploadLimit = 64 << 20
	}
	if len(o.TrustedProxies) == 0 {
		o.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")}
	}
	if err = os.MkdirAll(filepath.Join(abs, "files"), 0700); err != nil {
		return nil, err
	}
	l := flock.New(filepath.Join(abs, ".lock"))
	ok, err := l.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("data directory is in use")
	}
	a := &App{lock: l, options: o, readers: map[string]int{}, retired: map[string]bool{}, sessions: map[[32]byte]session{}, hashSlot: make(chan struct{}, 1), origin: http.NewCrossOriginProtection()}
	defer func() {
		if err != nil {
			a.Close()
		}
	}()
	a.db, err = openDB(filepath.Join(abs, "app.db"), "FULL", 5000)
	if err != nil {
		return nil, err
	}
	var version int
	err = a.db.QueryRow("PRAGMA user_version").Scan(&version)
	if err != nil {
		return nil, err
	}
	if version > 1 {
		err = fmt.Errorf("unsupported database version %d", version)
		return nil, err
	}
	_, err = a.db.Exec(`BEGIN; CREATE TABLE IF NOT EXISTS config (id INTEGER PRIMARY KEY CHECK(id=1), body TEXT NOT NULL); CREATE TABLE IF NOT EXISTS routes (id TEXT PRIMARY KEY, body TEXT NOT NULL); CREATE TABLE IF NOT EXISTS files (id TEXT PRIMARY KEY, route_id TEXT NOT NULL UNIQUE REFERENCES routes(id) ON DELETE CASCADE, name TEXT NOT NULL, size INTEGER NOT NULL); PRAGMA user_version=1; COMMIT;`)
	if err != nil {
		return nil, err
	}
	_, err = a.db.Exec(`CREATE TABLE IF NOT EXISTS administrator(id INTEGER PRIMARY KEY CHECK(id=1),username TEXT NOT NULL,password TEXT NOT NULL); CREATE TABLE IF NOT EXISTS tokens(id TEXT PRIMARY KEY,name TEXT NOT NULL,created INTEGER NOT NULL,digest TEXT NOT NULL UNIQUE);`)
	if err != nil {
		return nil, err
	}
	err = a.loadTokens()
	if err != nil {
		return nil, err
	}
	_, err = a.db.Exec(`CREATE TABLE IF NOT EXISTS timezone(id INTEGER PRIMARY KEY CHECK(id=1),name TEXT NOT NULL,data BLOB);`)
	if err != nil {
		return nil, err
	}
	if err = a.loadTimezone(); err != nil {
		return nil, err
	}
	c := Config{Routes: []Route{}, Builtins: map[string]bool{"ip": false, "time": false, "random": false}, Settings: Settings{RetentionDays: 30}}
	var body string
	e := a.db.QueryRow("SELECT body FROM config WHERE id=1").Scan(&body)
	if e == nil {
		err = json.Unmarshal([]byte(body), &c)
	} else if e != sql.ErrNoRows {
		err = e
	}
	if err != nil {
		return nil, err
	}
	s, e := BuildSnapshot(c)
	if e != nil {
		err = e
		return nil, err
	}
	a.snapshot.Store(s)
	err = a.recoverFiles()
	if err != nil {
		return nil, err
	}
	if err = a.initStats(); err != nil {
		return nil, err
	}
	return a, nil
}
func (a *App) Config() Config      { return cloneConfig(a.snapshot.Load().Config) }
func (a *App) Save(c Config) error { a.mu.Lock(); defer a.mu.Unlock(); return a.saveLocked(c) }
func (a *App) saveLocked(c Config) error {
	s, err := BuildSnapshot(c)
	if err != nil {
		return err
	}
	if err := validateFiles(s.Config); err != nil {
		return err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, _ := json.Marshal(s.Config)
	if _, err = tx.Exec("INSERT INTO config VALUES(1,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", string(b)); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM routes"); err != nil {
		return err
	}
	for _, r := range s.Config.Routes {
		b, _ = json.Marshal(r)
		if _, err = tx.Exec("INSERT INTO routes VALUES(?,?)", r.ID, string(b)); err != nil {
			return err
		}
		if r.Action.Source == "managed" && r.Action.FilePath != "" {
			if _, err = tx.Exec("INSERT INTO files VALUES(?,?,?,?)", r.Action.FilePath, r.ID, r.Action.FileName, r.Action.FileSize); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.publish(s)
	return nil
}
func (a *App) Close() error {
	var err error
	a.closeOnce.Do(func() {
		if a.statsDone != nil {
			select {
			case <-a.statsDone:
			default:
				if a.statsStop != nil {
					close(a.statsStop)
					<-a.statsDone
				}
			}
		}
		if a.statsRead != nil {
			_ = a.statsRead.Close()
		}
		if a.statsDB != nil {
			_ = a.statsDB.Close()
		}
		if a.db != nil {
			err = a.db.Close()
		}
		if a.lock != nil {
			_ = a.lock.Unlock()
		}
	})
	return err
}
