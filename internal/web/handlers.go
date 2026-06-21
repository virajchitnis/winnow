package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"winnow/internal/store"
)

// --- Auth pages ---------------------------------------------------------------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if err := s.verifyCloudflareAccess(r); err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if s.checkPassword(r.FormValue("password")) {
			s.issueSession(w, r)
			redirect(w, r, "/", "")
			return
		}
		s.render(w, r, "login", "Sign in", "", map[string]any{"Error": "Incorrect password."})
		return
	}
	s.render(w, r, "login", "Sign in", "", nil)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearSession(w)
	redirect(w, r, "/login", "")
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	status := http.StatusOK
	var h = s.sched.HealthSnapshot()
	if !h.LastPollAt.IsZero() && !h.LastPollOK {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"last_poll_ok":%t,"running":%t}`, h.LastPollOK, h.Running)
}

// --- Review tab ---------------------------------------------------------------

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	const pageSize = 50
	offset := 0
	if n, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && n > 0 {
		offset = n
	}
	// Fetch one extra to know whether an older page exists.
	decisions, _ := s.store.DecisionsPage(pageSize+1, offset)
	hasMore := len(decisions) > pageSize
	if hasMore {
		decisions = decisions[:pageSize]
	}

	// Format timestamps in the configured timezone for display.
	loc := time.UTC
	if st, err := s.store.LoadSettings(s.defaults); err == nil {
		if l, lerr := time.LoadLocation(st.Timezone); lerr == nil {
			loc = l
		}
	}
	type reviewRow struct {
		store.Decision
		When string
	}
	rows := make([]reviewRow, len(decisions))
	for i, d := range decisions {
		when := d.CreatedAt
		if ts, err := time.Parse(time.RFC3339Nano, d.CreatedAt); err == nil {
			when = ts.In(loc).Format("2006-01-02 15:04")
		}
		rows[i] = reviewRow{Decision: d, When: when}
	}

	cats, _ := s.store.Categories()
	today, _ := s.store.LLMCallsToday()
	prevOffset := offset - pageSize
	if prevOffset < 0 {
		prevOffset = 0
	}
	s.render(w, r, "review", "Review", "review", map[string]any{
		"Decisions":  rows,
		"Categories": cats,
		"LLMToday":   today,
		"HasPrev":    offset > 0,
		"HasMore":    hasMore,
		"PrevOffset": prevOffset,
		"NextOffset": offset + pageSize,
		"RangeStart": offset + 1,
		"RangeEnd":   offset + len(rows),
	})
}

func (s *Server) handleCorrect(w http.ResponseWriter, r *http.Request) {
	emailID := r.FormValue("email_id")
	sender := r.FormValue("sender")
	category := r.FormValue("category")
	if emailID == "" || category == "" {
		redirect(w, r, "/", "Missing fields.")
		return
	}
	// Record the correction as a sender observation so the classifier learns,
	// and (for a bulk category) as a deny-bulk rule override.
	domain := domainOf(sender)
	_ = s.store.RecordObservation(sender, domain, category)
	if cat, ok, _ := s.store.CategoryByName(category); ok {
		if cat.KeepInInbox {
			if sender != "" {
				_ = s.store.AddSenderRule(sender, store.KindAllowImportant, "")
			}
		} else if domain != "" {
			_ = s.store.AddSenderRule("@"+domain, store.KindDenyBulk, category)
		}
	}
	redirect(w, r, "/", "Recorded correction; Winnow will treat this sender as "+category+".")
}

// --- Categories tab -----------------------------------------------------------

func (s *Server) handleCategories(w http.ResponseWriter, r *http.Request) {
	cats, _ := s.store.Categories()
	s.render(w, r, "categories", "Categories", "categories", map[string]any{"Categories": cats})
}

func (s *Server) handleCategorySave(w http.ResponseWriter, r *http.Request) {
	c := store.Category{
		Name:              strings.TrimSpace(r.FormValue("name")),
		DestinationFolder: strings.TrimSpace(r.FormValue("destination_folder")),
		KeepInInbox:       r.FormValue("keep_in_inbox") == "on",
		Flag:              r.FormValue("flag") == "on",
		MarkRead:          r.FormValue("mark_read") == "on",
	}
	if c.Name == "" {
		redirect(w, r, "/categories", "Name is required.")
		return
	}
	if idStr := r.FormValue("id"); idStr != "" {
		id, _ := strconv.ParseInt(idStr, 10, 64)
		c.ID = id
		if err := s.store.UpdateCategory(c); err != nil {
			redirect(w, r, "/categories", "Error: "+err.Error())
			return
		}
	} else if _, err := s.store.CreateCategory(c); err != nil {
		redirect(w, r, "/categories", "Error: "+err.Error())
		return
	}
	redirect(w, r, "/categories", "Saved.")
}

func (s *Server) handleCategoryDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	_ = s.store.DeleteCategory(id)
	redirect(w, r, "/categories", "Deleted (built-in categories are protected).")
}

// --- Senders tab --------------------------------------------------------------

func (s *Server) handleSenders(w http.ResponseWriter, r *http.Request) {
	rules, _ := s.store.SenderRules()
	s.render(w, r, "senders", "Senders", "senders", map[string]any{"Rules": rules})
}

func (s *Server) handleSenderSave(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("delete") != "" {
		_ = s.store.DeleteSenderRule(r.FormValue("pattern"), r.FormValue("kind"))
		redirect(w, r, "/senders", "Removed.")
		return
	}
	pattern := strings.TrimSpace(r.FormValue("pattern"))
	kind := r.FormValue("kind")
	if pattern == "" || (kind != store.KindAllowImportant && kind != store.KindDenyBulk) {
		redirect(w, r, "/senders", "Invalid rule.")
		return
	}
	_ = s.store.AddSenderRule(pattern, kind, r.FormValue("category"))
	redirect(w, r, "/senders", "Saved.")
}

// --- Rules tab (Sieve) --------------------------------------------------------

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	proposed, _ := s.store.SieveCandidates(store.SieveProposed)
	approved, _ := s.store.SieveCandidates(store.SieveApproved)
	var block string
	if s.sieve != nil {
		block, _ = s.sieve.Preview()
	}
	s.render(w, r, "rules", "Rules", "rules", map[string]any{
		"Proposed": proposed,
		"Approved": approved,
		"Preview":  block,
	})
}

func (s *Server) handleRuleDecision(w http.ResponseWriter, r *http.Request) {
	domain := r.FormValue("domain")
	category := r.FormValue("category")
	status := r.FormValue("status") // approved | rejected
	if status != store.SieveApproved && status != store.SieveRejected {
		redirect(w, r, "/rules", "Invalid status.")
		return
	}
	_ = s.store.SetSieveCandidateStatus(domain, category, status)
	redirect(w, r, "/rules", "Updated.")
}

func (s *Server) handleRulesApply(w http.ResponseWriter, r *http.Request) {
	if s.sieve == nil {
		redirect(w, r, "/rules", "Sieve not available.")
		return
	}
	if err := s.sieve.Apply(r.Context()); err != nil {
		redirect(w, r, "/rules", "Error: "+err.Error())
		return
	}
	redirect(w, r, "/rules", "Applied approved rules to Fastmail.")
}

func (s *Server) handleRulesRevert(w http.ResponseWriter, r *http.Request) {
	if s.sieve == nil {
		redirect(w, r, "/rules", "Sieve not available.")
		return
	}
	if err := s.sieve.Revert(r.Context()); err != nil {
		redirect(w, r, "/rules", "Error: "+err.Error())
		return
	}
	redirect(w, r, "/rules", "Reverted to the previous script.")
}

// --- Unsubscribe tab ----------------------------------------------------------

func (s *Server) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	records, _ := s.store.UnsubscribeCandidates(statusFilter)
	s.render(w, r, "unsubscribe", "Unsubscribe", "unsubscribe", map[string]any{
		"Records": records,
		"Filter":  statusFilter,
	})
}

func (s *Server) handleUnsubDecision(w http.ResponseWriter, r *http.Request) {
	sender := r.FormValue("sender")
	choice := r.FormValue("choice") // unsubscribe | keep
	rec, ok, _ := s.store.UnsubscribeRecordBySender(sender)
	if !ok {
		redirect(w, r, "/unsubscribe", "Unknown sender.")
		return
	}
	switch choice {
	case "keep":
		category := r.FormValue("category")
		if category != "" {
			if cat, ok, _ := s.store.CategoryByName(category); ok && !cat.KeepInInbox {
				_ = s.store.AddSenderRule("@"+domainOf(sender), store.KindDenyBulk, category)
			}
		}
		_ = s.store.SetUnsubscribeStatus(sender, store.UnsubKept, false)
		redirect(w, r, "/unsubscribe", "Keeping "+sender+".")
	case "unsubscribe":
		if s.unsub == nil {
			redirect(w, r, "/unsubscribe", "Unsubscribe executor not available.")
			return
		}
		err := s.unsub.Execute(r.Context(), rec.Method, rec.Target)
		if err != nil {
			_ = s.store.SetUnsubscribeStatus(sender, store.UnsubNeedsDecision, false)
			redirect(w, r, "/unsubscribe", "Could not auto-unsubscribe: "+err.Error())
			return
		}
		_ = s.store.SetUnsubscribeStatus(sender, store.UnsubUnsubscribed, true)
		redirect(w, r, "/unsubscribe", "Unsubscribed from "+sender+".")
	default:
		redirect(w, r, "/unsubscribe", "Invalid choice.")
	}
}

// --- Controls actions ---------------------------------------------------------

func (s *Server) handleRunNow(w http.ResponseWriter, r *http.Request) {
	go s.sched.TriageOnce(contextDetached())
	redirect(w, r, "/", "Triage started.")
}

func (s *Server) handleSweep(w http.ResponseWriter, r *http.Request) {
	apply := r.FormValue("apply") == "on"
	go func() {
		_, _ = s.sched.Sweep(contextDetached(), apply)
	}()
	mode := "preview"
	if apply {
		mode = "apply"
	}
	redirect(w, r, "/", "Inbox sweep started ("+mode+").")
}

func urlEncode(s string) string { return url.QueryEscape(s) }
