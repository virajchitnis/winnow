package web

import (
	"bytes"
	"net/http"

	"winnow/internal/schedule"
	"winnow/internal/store"
)

// pageData is the model passed to every page template.
type pageData struct {
	Title     string
	ActiveTab string
	Authed    bool
	DryRun    bool
	Errors    []store.AppError
	Health    schedule.Health
	Flash     string
	Data      any
}

// render executes a page template within the layout. It enriches the model with
// the error banner, dry-run state, and health.
func (s *Server) render(w http.ResponseWriter, r *http.Request, page, title, tab string, data any) {
	pd := pageData{
		Title:     title,
		ActiveTab: tab,
		Authed:    s.hasValidSession(r),
		Flash:     r.URL.Query().Get("flash"),
		Data:      data,
	}
	if pd.Authed {
		if errs, err := s.store.ActiveErrors(20); err == nil {
			pd.Errors = errs
		}
		if st, err := s.store.LoadSettings(s.defaults); err == nil {
			pd.DryRun = st.DryRun
		}
		if s.sched != nil {
			pd.Health = s.sched.HealthSnapshot()
		}
	}

	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "template not found: "+page, http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout.html", pd); err != nil {
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// redirect issues a see-other redirect, optionally with a flash message.
func redirect(w http.ResponseWriter, r *http.Request, to, flash string) {
	if flash != "" {
		to += "?flash=" + urlEncode(flash)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}
