package pathbay

import (
	"net/http"
	"net/url"
)

func (s *Snapshot) publicError(w http.ResponseWriter, r *http.Request, code int) {
	if code != http.StatusNotFound || len(s.notFound) == 0 {
		http.Error(w, http.StatusText(code), code)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusNotFound)
	if r.Method != "HEAD" {
		_, _ = w.Write(s.notFound)
	}
}

func (a *App) respond(w http.ResponseWriter, r *http.Request, route *Route) {
	v := route.Action
	switch v.Type {
	case "redirect":
		target := *route.target
		if v.PreserveQuery == nil || *v.PreserveQuery {
			values, err := url.ParseQuery(r.URL.RawQuery)
			if err != nil {
				http.Error(w, "invalid query", 400)
				return
			}
			q := target.Query()
			for key, list := range values {
				q[key] = list
			}
			target.RawQuery = q.Encode()
		}
		w.Header().Set("Location", target.String())
		w.WriteHeader(v.Status)
	case "text", "json":
		ct := "text/plain; charset=utf-8"
		if v.Type == "json" {
			ct = "application/json; charset=utf-8"
		} else if v.Format == "html" {
			ct = "text/html; charset=utf-8"
		}
		w.Header().Set("Content-Type", ct)
		if r.Method != "HEAD" {
			_, _ = w.Write(route.body)
		}
	}
}
