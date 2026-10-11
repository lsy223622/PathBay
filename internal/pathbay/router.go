package pathbay

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

type Snapshot struct {
	Config   Config
	exact    map[string][]*Route
	prefixes []*Route
	ids      map[string]bool
}

func BuildSnapshot(c Config) (*Snapshot, error) {
	c = cloneConfig(c)
	s := &Snapshot{Config: c, exact: map[string][]*Route{}, ids: map[string]bool{}}
	unique := map[string]bool{}
	if c.Settings.RetentionDays < 0 {
		return nil, fmt.Errorf("retention must be nonnegative")
	}
	for k := range c.Builtins {
		if k != "ip" && k != "time" && k != "random" {
			return nil, fmt.Errorf("unknown builtin")
		}
	}
	for i := range s.Config.Routes {
		r := &s.Config.Routes[i]
		if err := validateRoute(r); err != nil {
			return nil, fmt.Errorf("route %s: %w", r.Path, err)
		}
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%t", r.Path, r.Method, r.Match, r.Boundary)
		if r.ID == "" || s.ids[r.ID] || unique[key] {
			return nil, fmt.Errorf("duplicate route or ID")
		}
		s.ids[r.ID] = true
		unique[key] = true
		if !r.Enabled {
			continue
		}
		if r.Match == "exact" {
			s.exact[r.Path] = append(s.exact[r.Path], r)
		} else {
			s.prefixes = append(s.prefixes, r)
		}
	}
	sort.Slice(s.prefixes, func(i, j int) bool {
		a, b := s.prefixes[i], s.prefixes[j]
		if len(a.Path) != len(b.Path) {
			return len(a.Path) > len(b.Path)
		}
		if a.Boundary != b.Boundary {
			return a.Boundary
		}
		return a.Method != "ANY" && b.Method == "ANY"
	})
	return s, nil
}
func supports(config, method string) bool {
	if method == "HEAD" {
		method = "GET"
	}
	if config == method {
		return true
	}
	return config == "ANY" && strings.Contains(" GET POST PUT PATCH DELETE OPTIONS ", " "+method+" ")
}
func pathMatch(r *Route, p string) bool {
	if r.Match == "exact" {
		return r.Path == p
	}
	return strings.HasPrefix(p, r.Path) && (!r.Boundary || strings.HasSuffix(r.Path, "/") || p == r.Path || strings.HasPrefix(p, r.Path+"/"))
}
func (s *Snapshot) Match(method, p string) (*Route, int, string) {
	if !strings.Contains(" GET HEAD POST PUT PATCH DELETE OPTIONS TRACE CONNECT ", " "+method+" ") {
		return nil, 501, ""
	}
	var any *Route
	for _, r := range s.exact[p] {
		if supports(r.Method, method) {
			if r.Method != "ANY" {
				return r, 200, ""
			}
			any = r
		}
	}
	if any != nil {
		return any, 200, ""
	}
	for _, r := range s.prefixes {
		if pathMatch(r, p) && supports(r.Method, method) {
			return r, 200, ""
		}
	}
	allowed := map[string]bool{}
	for _, r := range s.Config.Routes {
		if r.Enabled && pathMatch(&r, p) {
			if r.Method == "ANY" {
				for _, m := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
					allowed[m] = true
				}
			} else {
				allowed[r.Method] = true
				if r.Method == "GET" {
					allowed["HEAD"] = true
				}
			}
		}
	}
	if len(allowed) == 0 {
		return nil, 404, ""
	}
	out := []string{}
	for _, m := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		if allowed[m] {
			out = append(out, m)
		}
	}
	return nil, 405, strings.Join(out, ", ")
}
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := normalizeDecoded(r.URL.Path)
	if err != nil {
		http.Error(w, "invalid path", 400)
		return
	}
	r.URL.Path = p
	if p == "/admin" {
		http.Redirect(w, r, "/admin/", 308)
		return
	}
	if strings.HasPrefix(p, "/admin/") {
		a.admin(w, r)
		return
	}
	s := a.snapshot.Load()
	if p == "/ip" || p == "/time" || p == "/random" {
		a.builtin(w, r, strings.TrimPrefix(p, "/"), s.Config.Builtins[strings.TrimPrefix(p, "/")])
		return
	}
	route, code, allow := s.Match(r.Method, p)
	if route == nil {
		if allow != "" {
			w.Header().Set("Allow", allow)
		}
		http.Error(w, http.StatusText(code), code)
		return
	}
	a.respond(w, r, route)
}
