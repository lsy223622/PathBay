package pathbay

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func jsonResponse(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}
func apiError(w http.ResponseWriter, code int, message string) {
	jsonResponse(w, code, map[string]any{"error": map[string]any{"code": strings.ToLower(strings.ReplaceAll(http.StatusText(code), " ", "_")), "message": message}})
}
func decodeBody(w http.ResponseWriter, r *http.Request, out any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}
func bodyError(w http.ResponseWriter, err error) {
	var size *http.MaxBytesError
	if errors.As(err, &size) {
		apiError(w, 413, "request body exceeds limit")
	} else {
		apiError(w, 400, err.Error())
	}
}
func page(r *http.Request) (int, int, error) {
	p, n := 1, 50
	var err error
	if v := r.URL.Query().Get("page"); v != "" {
		p, err = strconv.Atoi(v)
		if err != nil || p < 1 {
			return 0, 0, fmt.Errorf("invalid page")
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err = strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			return 0, 0, fmt.Errorf("limit must be 1–200")
		}
	}
	return p, n, nil
}
func (a *App) admin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !strings.HasPrefix(r.URL.Path, "/admin/api/v1/") {
		http.Error(w, "management UI initializing", 503)
		return
	}
	endpoint := strings.TrimPrefix(r.URL.Path, "/admin/api/v1/")
	if r.Method == "OPTIONS" {
		w.Header().Set("Allow", "GET, HEAD, POST, PATCH, PUT, DELETE, OPTIONS")
		w.WriteHeader(204)
		return
	}
	if !(r.Method == "PUT" && strings.HasSuffix(endpoint, "/file")) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
	}
	csrf, authenticated := a.authenticate(r)
	public := endpoint == "setup" || endpoint == "login" || endpoint == "session"
	if !public && !authenticated {
		apiError(w, 401, "authentication required")
		return
	}
	if r.Header.Get("Authorization") == "" {
		if err := a.origin.Check(r); err != nil {
			apiError(w, 403, "cross-site request rejected")
			return
		}
		if authenticated && r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-CSRF-Token") != csrf {
			apiError(w, 403, "CSRF token required")
			return
		}
	}
	if endpoint == "session" && r.Method == "GET" {
		var count int
		err := a.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM administrator").Scan(&count)
		if err != nil {
			apiError(w, 503, "database unavailable")
			return
		}
		jsonResponse(w, 200, map[string]any{"initialized": count > 0, "authenticated": authenticated, "csrf": csrf})
		return
	}
	if endpoint == "setup" || endpoint == "login" {
		if r.Method != "POST" {
			apiError(w, 405, "use POST")
			return
		}
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decodeBody(w, r, &input, 16<<10); err != nil {
			bodyError(w, err)
			return
		}
		var username, hash string
		err := a.db.QueryRowContext(r.Context(), "SELECT username,password FROM administrator WHERE id=1").Scan(&username, &hash)
		if endpoint == "setup" {
			if err == nil {
				apiError(w, 409, "administrator already initialized")
				return
			}
			if err != sql.ErrNoRows {
				apiError(w, 503, "database unavailable")
				return
			}
			if len(strings.TrimSpace(input.Username)) == 0 || len(input.Username) > 100 {
				apiError(w, 400, "username required (max 100 bytes)")
				return
			}
			hash, err = a.hashPassword(input.Password)
			if err != nil {
				code := 400
				if strings.Contains(err.Error(), "busy") {
					code = 429
				}
				apiError(w, code, err.Error())
				return
			}
			a.mu.Lock()
			_, err = a.db.ExecContext(r.Context(), "INSERT INTO administrator VALUES(1,?,?)", input.Username, hash)
			a.mu.Unlock()
			if err != nil {
				apiError(w, 409, "administrator already initialized")
				return
			}
			jsonResponse(w, 201, map[string]any{"csrf": a.newSession(w, r)})
			return
		}
		if err != nil {
			apiError(w, 401, "invalid username or password")
			return
		}
		valid, e := a.verifyPassword(input.Password, hash)
		if e != nil {
			apiError(w, 429, e.Error())
			return
		}
		if !valid || input.Username != username {
			apiError(w, 401, "invalid username or password")
			return
		}
		jsonResponse(w, 200, map[string]any{"csrf": a.newSession(w, r)})
		return
	}
	switch {
	case endpoint == "logout" && r.Method == "POST":
		if c, e := r.Cookie("pathbay_session"); e == nil {
			a.authMu.Lock()
			delete(a.sessions, sha256.Sum256([]byte(c.Value)))
			a.authMu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: "pathbay_session", Value: "", Path: "/admin/", HttpOnly: true, Secure: a.isHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: -1})
		w.WriteHeader(204)
	case endpoint == "password" && r.Method == "POST":
		var input struct {
			Current  string `json:"current_password"`
			Password string `json:"password"`
		}
		if err := decodeBody(w, r, &input, 16<<10); err != nil {
			bodyError(w, err)
			return
		}
		var hash string
		if err := a.db.QueryRow("SELECT password FROM administrator WHERE id=1").Scan(&hash); err != nil {
			apiError(w, 503, err.Error())
			return
		}
		valid, err := a.verifyPassword(input.Current, hash)
		if err != nil {
			apiError(w, 429, err.Error())
			return
		}
		if !valid {
			apiError(w, 403, "current password incorrect")
			return
		}
		if err = a.ResetPassword(input.Password); err != nil {
			apiError(w, 400, err.Error())
			return
		}
		w.WriteHeader(204)
	case endpoint == "tokens" && r.Method == "GET":
		jsonResponse(w, 200, a.ListTokens())
	case endpoint == "tokens" && r.Method == "POST":
		var input struct {
			Name string `json:"name"`
		}
		if err := decodeBody(w, r, &input, 16<<10); err != nil {
			bodyError(w, err)
			return
		}
		t, value, err := a.CreateToken(input.Name)
		if err != nil {
			apiError(w, 400, err.Error())
			return
		}
		jsonResponse(w, 201, map[string]any{"id": t.ID, "name": t.Name, "created": t.Created, "token": value})
	case strings.HasPrefix(endpoint, "tokens/") && r.Method == "DELETE":
		if err := a.RevokeToken(strings.TrimPrefix(endpoint, "tokens/")); err != nil {
			apiError(w, 404, "token not found")
			return
		}
		w.WriteHeader(204)
	case endpoint == "builtins" || endpoint == "settings":
		if r.Method == "GET" {
			c := a.Config()
			if endpoint == "builtins" {
				jsonResponse(w, 200, c.Builtins)
			} else {
				jsonResponse(w, 200, c.Settings)
			}
			return
		}
		if r.Method != "PATCH" {
			apiError(w, 405, "use GET or PATCH")
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		c := a.Config()
		var err error
		if endpoint == "builtins" {
			err = decodeBody(w, r, &c.Builtins, 16<<10)
		} else {
			err = decodeBody(w, r, &c.Settings, 16<<10)
		}
		if err != nil {
			bodyError(w, err)
			return
		}
		if err = a.saveLocked(c); err != nil {
			apiError(w, 400, err.Error())
			return
		}
		if endpoint == "builtins" {
			jsonResponse(w, 200, c.Builtins)
		} else {
			jsonResponse(w, 200, c.Settings)
		}
	case endpoint == "routes" || strings.HasPrefix(endpoint, "routes/"):
		a.routeAPI(w, r, endpoint)
	default:
		apiError(w, 404, "resource not found")
	}
}
func (a *App) routeAPI(w http.ResponseWriter, r *http.Request, endpoint string) {
	id := strings.TrimPrefix(endpoint, "routes/")
	if strings.HasSuffix(id, "/file") && r.Method == "PUT" {
		id = strings.TrimSuffix(id, "/file")
		r.Body = http.MaxBytesReader(w, r.Body, a.options.UploadLimit+(1<<20))
		reader, err := r.MultipartReader()
		if err != nil {
			bodyError(w, err)
			return
		}
		part, err := reader.NextPart()
		if err != nil {
			bodyError(w, err)
			return
		}
		defer part.Close()
		if part.FileName() == "" {
			apiError(w, 400, "file part required")
			return
		}
		if err = a.Upload(id, part.FileName(), part); err != nil {
			code := 400
			if os.IsNotExist(err) {
				code = 404
			} else if strings.Contains(err.Error(), "limit") {
				code = 413
			}
			apiError(w, code, err.Error())
			return
		}
		for _, route := range a.Config().Routes {
			if route.ID == id {
				jsonResponse(w, 200, route)
				return
			}
		}
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.Config()
	index := -1
	for i, route := range c.Routes {
		if route.ID == id {
			index = i
			break
		}
	}
	if endpoint == "routes" && r.Method == "GET" {
		p, n, err := page(r)
		if err != nil {
			apiError(w, 400, err.Error())
			return
		}
		start := min((p-1)*n, len(c.Routes))
		end := min(start+n, len(c.Routes))
		jsonResponse(w, 200, map[string]any{"items": c.Routes[start:end], "total": len(c.Routes), "page": p})
		return
	}
	if endpoint != "routes" && index < 0 {
		apiError(w, 404, "route not found")
		return
	}
	if r.Method == "GET" {
		jsonResponse(w, 200, c.Routes[index])
		return
	}
	if r.Method == "DELETE" && index >= 0 {
		c.Routes = append(c.Routes[:index], c.Routes[index+1:]...)
		if err := a.saveLocked(c); err != nil {
			apiError(w, 503, err.Error())
			return
		}
		a.cleanFiles()
		w.WriteHeader(204)
		return
	}
	if (endpoint == "routes" && r.Method == "POST") || (index >= 0 && r.Method == "PATCH") {
		route := Route{ID: newID(), Method: "GET", Match: "exact", Boundary: true, Enabled: true}
		if index >= 0 {
			route = c.Routes[index]
		}
		var raw json.RawMessage
		if err := decodeBody(w, r, &raw, 2<<20); err != nil {
			bodyError(w, err)
			return
		}
		if err := strictDecode(raw, &route); err != nil {
			bodyError(w, err)
			return
		}
		var keys map[string]json.RawMessage
		json.Unmarshal(raw, &keys)
		if _, ok := keys["id"]; ok {
			apiError(w, 400, "route ID is read-only")
			return
		}
		if _, ok := keys["path"]; ok {
			p, err := NormalizePath(route.Path)
			if err != nil {
				apiError(w, 400, err.Error())
				return
			}
			route.Path = p
		}
		if route.Action.Source == "managed" {
			previous := ""
			if index >= 0 {
				previous = c.Routes[index].Action.FilePath
			}
			if route.Action.FilePath != previous {
				apiError(w, 400, "managed reference changes require file upload")
				return
			}
		}
		code := 200
		if index < 0 {
			c.Routes = append(c.Routes, route)
			code = 201
		} else {
			c.Routes[index] = route
		}
		if err := a.saveLocked(c); err != nil {
			status := 400
			if strings.Contains(err.Error(), "duplicate") {
				status = 409
			}
			apiError(w, status, err.Error())
			return
		}
		a.cleanFiles()
		for _, saved := range a.Config().Routes {
			if saved.ID == route.ID {
				jsonResponse(w, code, saved)
				return
			}
		}
		return
	}
	apiError(w, 405, "method not allowed")
}
func strictDecode(b []byte, out any) error {
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
