package pathbay

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

func (a *App) statsAPI(w http.ResponseWriter, r *http.Request, endpoint string) {
	if r.Method != "GET" {
		apiError(w, 405, "use GET")
		return
	}
	_, limit, err := page(r)
	if err != nil {
		apiError(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	switch endpoint {
	case "stats/summary":
		rows, e := a.statsRead.QueryContext(ctx, "SELECT kind,hit_id,hits,last FROM summary ORDER BY kind,hit_id")
		if e != nil {
			apiError(w, 503, e.Error())
			return
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var kind, id string
			var hits, last int64
			if e = rows.Scan(&kind, &id, &hits, &last); e != nil {
				apiError(w, 503, e.Error())
				return
			}
			items = append(items, map[string]any{"kind": kind, "hit_id": id, "hits": hits, "last": last})
		}
		if e = rows.Err(); e != nil {
			apiError(w, 503, e.Error())
			return
		}
		warning := ""
		if v := a.statsFailure.Load(); v != nil {
			warning = *v
		}
		jsonResponse(w, 200, map[string]any{"items": items, "dropped_logs": a.droppedLogs.Load(), "lost_hits": a.lostHits.Load(), "write_error": warning, "timezone": a.zone.Load().String()})
	case "stats/daily":
		days := 30
		if value := r.URL.Query().Get("days"); value != "" {
			days, err = strconv.Atoi(value)
			if err != nil || days < 1 || days > 366 {
				apiError(w, 400, "days must be 1–366")
				return
			}
		}
		since := time.Now().In(a.zone.Load()).AddDate(0, 0, -days+1).Format("2006-01-02")
		rows, e := a.statsRead.QueryContext(ctx, "SELECT day,sum(hits) FROM daily WHERE day>=? AND (?='' OR hit_id=?) GROUP BY day ORDER BY day", since, r.URL.Query().Get("hit_id"), r.URL.Query().Get("hit_id"))
		if e != nil {
			apiError(w, 503, e.Error())
			return
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var day string
			var hits int64
			if e = rows.Scan(&day, &hits); e != nil {
				apiError(w, 503, e.Error())
				return
			}
			items = append(items, map[string]any{"day": day, "hits": hits})
		}
		if e = rows.Err(); e != nil {
			apiError(w, 503, e.Error())
			return
		}
		jsonResponse(w, 200, items)
	case "stats/logs":
		q := r.URL.Query()
		cursorTime, cursorID := int64(1<<63-1), int64(1<<63-1)
		if cursor := q.Get("cursor"); cursor != "" {
			if n, e := fmt.Sscanf(cursor, "%d:%d", &cursorTime, &cursorID); e != nil || n != 2 {
				apiError(w, 400, "invalid cursor")
				return
			}
		}
		from, to := int64(0), int64(1<<63-1)
		for key, out := range map[string]*int64{"from": &from, "to": &to} {
			if value := q.Get(key); value != "" {
				at, e := time.Parse(time.RFC3339, value)
				if e != nil {
					apiError(w, 400, "invalid "+key)
					return
				}
				*out = at.UnixMilli()
			}
		}
		rows, e := a.statsRead.QueryContext(ctx, `SELECT id,time,kind,hit_id,ip,user_agent,referer,path,method,status FROM logs WHERE (time<? OR (time=? AND id<?)) AND time>=? AND time<=? AND (?='' OR hit_id=?) ORDER BY time DESC,id DESC LIMIT ?`, cursorTime, cursorTime, cursorID, from, to, q.Get("hit_id"), q.Get("hit_id"), limit+1)
		if e != nil {
			apiError(w, 503, e.Error())
			return
		}
		defer rows.Close()
		items := []Visit{}
		for rows.Next() {
			var v Visit
			if e = rows.Scan(&v.ID, &v.Time, &v.Kind, &v.HitID, &v.IP, &v.UserAgent, &v.Referer, &v.Path, &v.Method, &v.Status); e != nil {
				apiError(w, 503, e.Error())
				return
			}
			items = append(items, v)
		}
		if e = rows.Err(); e != nil {
			apiError(w, 503, e.Error())
			return
		}
		next := ""
		if len(items) > limit {
			items = items[:limit]
			v := items[len(items)-1]
			next = fmt.Sprintf("%d:%d", v.Time, v.ID)
		}
		jsonResponse(w, 200, map[string]any{"items": items, "next_cursor": next})
	default:
		apiError(w, 404, "statistics resource not found")
	}
}
