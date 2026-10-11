package pathbay

import (
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var zoneCache = struct {
	sync.Mutex
	zones map[string]*time.Location
	order []string
}{zones: map[string]*time.Location{}}

func namedZone(name string) (*time.Location, error) {
	zoneCache.Lock()
	defer zoneCache.Unlock()
	if z := zoneCache.zones[name]; z != nil {
		return z, nil
	}
	z, err := time.LoadLocation(name)
	if err != nil {
		return nil, err
	}
	if len(zoneCache.order) >= 64 {
		delete(zoneCache.zones, zoneCache.order[0])
		zoneCache.order = zoneCache.order[1:]
	}
	zoneCache.order = append(zoneCache.order, name)
	zoneCache.zones[name] = z
	return z, nil
}
func (a *App) peer(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, _ := netip.ParseAddr(host)
	ip = ip.Unmap()
	for _, p := range a.options.TrustedProxies {
		if p.Contains(ip) {
			return ip, true
		}
	}
	return ip, false
}
func (a *App) clientIP(r *http.Request) string {
	ip, trusted := a.peer(r)
	if trusted {
		if real, err := netip.ParseAddr(r.Header.Get("X-Real-IP")); err == nil {
			ip = real.Unmap()
		}
	}
	return ip.String()
}
func (a *App) isHTTPS(r *http.Request) bool {
	_, trusted := a.peer(r)
	return r.TLS != nil || (trusted && r.Header.Get("X-Forwarded-Proto") == "https")
}
func parameter(q url.Values, key, fallback string) (string, error) {
	v, ok := q[key]
	if !ok {
		return fallback, nil
	}
	if len(v) != 1 {
		return "", fmt.Errorf("repeated %s", key)
	}
	return v[0], nil
}
func randomString(template string, n int) (string, error) {
	if template == "uuid" {
		return newID(), nil
	}
	alphabets := map[string]string{"binary": "01", "decimal": "0123456789", "hex": "0123456789abcdef", "lower": "0123456789abcdefghijklmnopqrstuvwxyz", "upper": "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ", "alnum": "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"}
	alphabet, ok := alphabets[template]
	if !ok {
		return "", fmt.Errorf("unknown template")
	}
	if n < 1 || n > 1024 {
		return "", fmt.Errorf("length must be 1–1024")
	}
	out := make([]byte, 0, n)
	buffer := make([]byte, 256)
	limit := 256 - 256%len(alphabet)
	for len(out) < n {
		if _, err := rand.Read(buffer); err != nil {
			return "", err
		}
		for _, b := range buffer {
			if int(b) < limit {
				out = append(out, alphabet[int(b)%len(alphabet)])
				if len(out) == n {
					break
				}
			}
		}
	}
	return string(out), nil
}
func (a *App) builtin(w http.ResponseWriter, r *http.Request, name string, enabled bool) {
	if !enabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", 405)
		return
	}
	var out string
	var err error
	switch name {
	case "ip":
		out = a.clientIP(r)
	case "time":
		q, e := url.ParseQuery(r.URL.RawQuery)
		err = e
		if err == nil {
			var tz, format string
			tz, err = parameter(q, "tz", "")
			if err == nil {
				format, err = parameter(q, "format", "rfc3339")
			}
			now := time.Now()
			if tz != "" && err == nil {
				var z *time.Location
				z, err = namedZone(tz)
				if err == nil {
					now = now.In(z)
				}
			}
			if err == nil {
				switch format {
				case "rfc3339":
					out = now.Format(time.RFC3339)
				case "datetime":
					out = now.Format("2006-01-02 15:04:05")
				case "unix":
					out = strconv.FormatInt(now.Unix(), 10)
				case "unix_ms":
					out = strconv.FormatInt(now.UnixMilli(), 10)
				default:
					err = fmt.Errorf("unknown time format")
				}
			}
		}
	case "random":
		q, e := url.ParseQuery(r.URL.RawQuery)
		err = e
		if err == nil {
			var template, length string
			template, err = parameter(q, "template", "alnum")
			n := 16
			if template != "uuid" && err == nil {
				length, err = parameter(q, "length", "16")
				if err == nil {
					n, err = strconv.Atoi(length)
				}
			}
			if err == nil {
				out, err = randomString(template, n)
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if r.Method != "HEAD" {
		_, _ = w.Write([]byte(strings.TrimSuffix(out, "\n") + "\n"))
	}
}
