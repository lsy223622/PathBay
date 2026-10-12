package pathbay

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"fmt"
	"golang.org/x/crypto/argon2"
	"net/http"
	"strings"
	"time"
)

type session struct {
	Digest           [32]byte
	CSRF             string
	Created, Expires time.Time
}
type Token struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Created int64  `json:"created"`
	Digest  string `json:"-"`
}

func secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func tokenDigest(v string) string { d := sha256.Sum256([]byte(v)); return fmt.Sprintf("%x", d) }
func (a *App) hashPassword(password string) (string, error) {
	select {
	case a.hashSlot <- struct{}{}:
		defer func() { <-a.hashSlot }()
	default:
		return "", fmt.Errorf("password hashing busy")
	}
	if len(password) < 12 || len(password) > 1024 {
		return "", fmt.Errorf("password must be 12–1024 bytes")
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	hash := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	return "$argon2id$v=19$m=19456,t=2,p=1$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(hash), nil
}
func (a *App) verifyPassword(password, phc string) (bool, error) {
	select {
	case a.hashSlot <- struct{}{}:
		defer func() { <-a.hashSlot }()
	default:
		return false, fmt.Errorf("password hashing busy")
	}
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=19456,t=2,p=1" {
		return false, fmt.Errorf("unsupported password hash")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	hash := argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
	return subtle.ConstantTimeCompare(hash, want) == 1, nil
}
func (a *App) newSession(w http.ResponseWriter, r *http.Request) string {
	value, csrf := secret(), secret()
	now := time.Now()
	s := session{Digest: sha256.Sum256([]byte(value)), CSRF: csrf, Created: now, Expires: now.Add(7 * 24 * time.Hour)}
	a.authMu.Lock()
	for id, v := range a.sessions {
		if !v.Expires.After(now) {
			delete(a.sessions, id)
		}
	}
	if len(a.sessions) >= 16 {
		var oldest [32]byte
		var at time.Time
		for id, v := range a.sessions {
			if at.IsZero() || v.Created.Before(at) {
				oldest = id
				at = v.Created
			}
		}
		delete(a.sessions, oldest)
	}
	a.sessions[s.Digest] = s
	a.authMu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "pathbay_session", Value: value, Path: "/admin/", HttpOnly: true, Secure: a.isHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600, Expires: s.Expires})
	return csrf
}
func (a *App) authenticate(r *http.Request) (string, bool) {
	if value := r.Header.Get("Authorization"); value != "" {
		if !strings.HasPrefix(value, "Bearer ") {
			return "", false
		}
		digest := tokenDigest(strings.TrimPrefix(value, "Bearer "))
		_, ok := (*a.tokens.Load())[digest]
		return "", ok
	}
	c, err := r.Cookie("pathbay_session")
	if err != nil {
		return "", false
	}
	id := sha256.Sum256([]byte(c.Value))
	a.authMu.Lock()
	s, ok := a.sessions[id]
	if ok && !s.Expires.After(time.Now()) {
		delete(a.sessions, id)
		ok = false
	}
	a.authMu.Unlock()
	return s.CSRF, ok
}
func (a *App) loadTokens() error {
	rows, err := a.db.Query("SELECT id,name,created,digest FROM tokens")
	if err != nil {
		return err
	}
	defer rows.Close()
	values := map[string]Token{}
	for rows.Next() {
		var t Token
		if err = rows.Scan(&t.ID, &t.Name, &t.Created, &t.Digest); err != nil {
			return err
		}
		values[t.Digest] = t
	}
	if err = rows.Err(); err != nil {
		return err
	}
	a.tokens.Store(&values)
	return nil
}
func (a *App) CreateToken(name string) (Token, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(strings.TrimSpace(name)) == 0 || len(name) > 200 {
		return Token{}, "", fmt.Errorf("token name required (max 200 bytes)")
	}
	value := secret()
	t := Token{ID: newID(), Name: name, Created: time.Now().UnixMilli(), Digest: tokenDigest(value)}
	values := map[string]Token{}
	for digest, existing := range *a.tokens.Load() {
		values[digest] = existing
	}
	values[t.Digest] = t
	_, err := a.db.Exec("INSERT INTO tokens VALUES(?,?,?,?)", t.ID, t.Name, t.Created, t.Digest)
	if err != nil {
		return Token{}, "", err
	}
	a.tokens.Store(&values)
	return t, value, nil
}
func (a *App) ListTokens() []Token {
	out := []Token{}
	for _, t := range *a.tokens.Load() {
		out = append(out, t)
	}
	return out
}
func (a *App) RevokeToken(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	values := map[string]Token{}
	for digest, existing := range *a.tokens.Load() {
		if existing.ID != id {
			values[digest] = existing
		}
	}
	res, err := a.db.Exec("DELETE FROM tokens WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	a.tokens.Store(&values)
	return nil
}
func (a *App) ResetPassword(password string) error {
	hash, err := a.hashPassword(password)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	res, err := a.db.Exec("UPDATE administrator SET password=? WHERE id=1", hash)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("administrator not initialized")
	}
	a.authMu.Lock()
	clear(a.sessions)
	a.authMu.Unlock()
	return nil
}
