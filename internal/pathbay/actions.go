package pathbay

import (
	"net/http"
	"net/url"
)

func (a *App) respond(w http.ResponseWriter, r *http.Request, route *Route) {
	v := route.Action
	switch v.Type {
	case "file", "directory":
		a.fileResponse(w, r)
	case "redirect":
		target, _ := url.Parse(v.Target)
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
			_, _ = w.Write([]byte(v.Content))
		}
	}
}
