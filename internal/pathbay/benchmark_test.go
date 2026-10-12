package pathbay

import (
	"fmt"
	"net/http/httptest"
	"testing"
)

func BenchmarkMatch(b *testing.B) {
	for _, count := range []int{10, 100, 1000} {
		for _, match := range []string{"exact", "prefix"} {
			b.Run(fmt.Sprintf("%s/%d", match, count), func(b *testing.B) {
				c := Config{}
				for i := 0; i < count; i++ {
					c.Routes = append(c.Routes, Route{ID: fmt.Sprint(i), Path: fmt.Sprintf("/route/%04d", i), Method: "GET", Match: match, Boundary: true, Enabled: true, Action: Action{Type: "text"}})
				}
				s, err := BuildSnapshot(c)
				if err != nil {
					b.Fatal(err)
				}
				p := fmt.Sprintf("/route/%04d", count-1)
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					s.Match("GET", p)
				}
			})
		}
	}
}
func BenchmarkPublicResponse(b *testing.B) {
	for _, logs := range []bool{false, true} {
		b.Run(fmt.Sprintf("logs=%t", logs), func(b *testing.B) {
			a, err := Open(Options{DataDir: b.TempDir()})
			if err != nil {
				b.Fatal(err)
			}
			defer a.Close()
			c := a.Config()
			c.Routes = []Route{{ID: "b", Path: "/bench", Method: "GET", Match: "exact", Enabled: true, Action: Action{Type: "text", Content: "hello"}}}
			c.Settings.DetailedLogs = logs
			if logs {
				c.Settings.Fields = LogFields{IP: true, UserAgent: true, Referer: true, Path: true, Method: true, Status: true}
			}
			a.Save(c)
			r := httptest.NewRequest("GET", "/bench", nil)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				w := httptest.NewRecorder()
				a.ServeHTTP(w, r)
			}
		})
	}
}
