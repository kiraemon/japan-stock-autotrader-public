package dashboard

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"sync"
	"time"
)

//go:embed web/*
var web embed.FS

type Loader func(context.Context, string) (View, error)
type cached struct {
	view View
	at   time.Time
}

// Handler binds rendering to a restricted projection. No file server or raw data route exists.
func Handler(port int, load Loader, readRuntime func(string) Runtime) http.Handler {
	tmpl := template.Must(template.New("index.html").Funcs(template.FuncMap{
		"clock": func(t time.Time) string { return t.In(time.FixedZone("JST", 9*3600)).Format("15:04:05") },
	}).ParseFS(web, "web/index.html"))
	var mu sync.Mutex
	cache := map[string]cached{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		allowed := r.Host == "127.0.0.1:"+strconv.Itoa(port) || r.Host == "localhost:"+strconv.Itoa(port)
		origin := r.Header.Get("Origin")
		if !allowed || (origin != "" && origin != "http://"+r.Host) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/assets/dashboard.css" || r.URL.Path == "/assets/dashboard.js" || r.URL.Path == "/assets/mark.svg" {
			name := "dashboard.css"
			contentType := "text/css; charset=utf-8"
			if r.URL.Path == "/assets/dashboard.js" {
				name = "dashboard.js"
				contentType = "text/javascript; charset=utf-8"
			}
			if r.URL.Path == "/assets/mark.svg" {
				name = "mark.svg"
				contentType = "image/svg+xml"
			}
			data, _ := web.ReadFile("web/" + name)
			w.Header().Set("Content-Type", contentType)
			if r.Method == http.MethodGet {
				_, _ = w.Write(data)
			}
			return
		}
		if r.URL.Path != "/" && r.URL.Path != "/api/status" {
			http.NotFound(w, r)
			return
		}
		environment := r.URL.Query().Get("environment")
		if environment == "" {
			environment = "production"
		}
		if environment != "production" && environment != "test" {
			http.Error(w, "Invalid environment", http.StatusBadRequest)
			return
		}
		// Reuse a per-environment disk projection for 10 seconds, regardless of browser refresh rate.
		mu.Lock()
		item, ok := cache[environment]
		if !ok || time.Since(item.at) >= 10*time.Second {
			view, err := load(r.Context(), environment)
			if err != nil {
				mu.Unlock()
				http.Error(w, "保存データを表示できません。保存先とファイル数を確認してください。", http.StatusServiceUnavailable)
				return
			}
			item = cached{view, time.Now()}
			cache[environment] = item
		}
		mu.Unlock()
		view := item.view
		// Heartbeat age must be assessed now, outside the slower snapshot cache.
		if readRuntime != nil {
			view.Runtime = readRuntime(environment)
		}
		var body bytes.Buffer
		if r.URL.Path == "/api/status" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			if json.NewEncoder(&body).Encode(view) != nil {
				http.Error(w, "表示結果を生成できません", 500)
				return
			}
		} else {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if tmpl.ExecuteTemplate(&body, "index.html", view) != nil {
				http.Error(w, "画面を生成できません", 500)
				return
			}
		}
		w.Header().Set("Content-Length", fmt.Sprint(body.Len()))
		if r.Method == http.MethodGet {
			_, _ = w.Write(body.Bytes())
		}
	})
}
