// Package web serves Winnow's dashboard: a server-rendered, responsive UI for
// reviewing/correcting decisions and tuning everything. It is protected by an
// app-password session and, for tunnel requests, Cloudflare Access JWT
// verification.
package web

import (
	"context"
	"embed"
	"html/template"
	"net/http"

	"winnow/internal/config"
	"winnow/internal/jmap"
	"winnow/internal/schedule"
	"winnow/internal/sieve"
	"winnow/internal/store"
	"winnow/internal/unsubscribe"
)

//go:embed templates/*.html
var templatesFS embed.FS

// Scheduler is the control surface the dashboard drives.
type Scheduler interface {
	TriageOnce(ctx context.Context)
	Sweep(ctx context.Context, apply bool) (schedule.SweepResult, error)
	Refile(ctx context.Context, emailID, category string) (string, error)
	ApplyReviewed(ctx context.Context) (int, error)
	HealthSnapshot() schedule.Health
}

// Pinger checks connectivity to an external service (for the test buttons).
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps bundles the dashboard's dependencies.
type Deps struct {
	Store         *store.Store
	Scheduler     Scheduler
	Sieve         *sieve.Generator
	Unsub         *unsubscribe.Executor
	JMAP          *jmap.Client
	FastmailPing  Pinger
	AnthropicPing Pinger
	Config        *config.Config
}

// Server is the dashboard HTTP server.
type Server struct {
	store         *store.Store
	sched         Scheduler
	sieve         *sieve.Generator
	unsub         *unsubscribe.Executor
	jmap          *jmap.Client
	fastmailPing  Pinger
	anthropicPing Pinger

	passwordHash  string
	sessionSecret string
	defaults      config.Settings
	cfVerifier    cfVerifier

	pages map[string]*template.Template
}

// New constructs the dashboard server.
func New(d Deps) (*Server, error) {
	s := &Server{
		store:         d.Store,
		sched:         d.Scheduler,
		sieve:         d.Sieve,
		unsub:         d.Unsub,
		jmap:          d.JMAP,
		fastmailPing:  d.FastmailPing,
		anthropicPing: d.AnthropicPing,
		passwordHash:  d.Config.AppPasswordHash,
		sessionSecret: d.Config.SessionSecret,
		defaults:      d.Config.Defaults,
	}
	if d.Config.CFAccessEnabled() {
		s.cfVerifier = NewCloudflareAccess(d.Config.CFAccessTeamDomain, d.Config.CFAccessAUD)
	}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	return s, nil
}

// Handler returns the dashboard's HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public.
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)

	// Authenticated pages.
	mux.HandleFunc("/", s.requireAuth(s.handleReview))
	mux.HandleFunc("/categories", s.requireAuth(s.handleCategories))
	mux.HandleFunc("/senders", s.requireAuth(s.handleSenders))
	mux.HandleFunc("/rules", s.requireAuth(s.handleRules))
	mux.HandleFunc("/unsubscribe", s.requireAuth(s.handleUnsubscribe))
	mux.HandleFunc("/settings", s.requireAuth(s.handleSettings))

	// Authenticated actions (POST).
	mux.HandleFunc("/action/correct", s.requireAuth(s.handleCorrect))
	mux.HandleFunc("/action/refile", s.requireAuth(s.handleRefile))
	mux.HandleFunc("/action/category", s.requireAuth(s.handleCategorySave))
	mux.HandleFunc("/action/category/delete", s.requireAuth(s.handleCategoryDelete))
	mux.HandleFunc("/action/sender", s.requireAuth(s.handleSenderSave))
	mux.HandleFunc("/action/rule", s.requireAuth(s.handleRuleDecision))
	mux.HandleFunc("/action/rules/apply", s.requireAuth(s.handleRulesApply))
	mux.HandleFunc("/action/rules/revert", s.requireAuth(s.handleRulesRevert))
	mux.HandleFunc("/action/unsub", s.requireAuth(s.handleUnsubDecision))
	mux.HandleFunc("/action/settings", s.requireAuth(s.handleSettingsSave))
	mux.HandleFunc("/action/password", s.requireAuth(s.handlePasswordChange))
	mux.HandleFunc("/action/run", s.requireAuth(s.handleRunNow))
	mux.HandleFunc("/action/sweep", s.requireAuth(s.handleSweep))
	mux.HandleFunc("/action/apply-reviewed", s.requireAuth(s.handleApplyReviewed))
	mux.HandleFunc("/action/test", s.requireAuth(s.handleTestConnection))

	return mux
}

func (s *Server) parseTemplates() error {
	pages := []string{
		"review", "categories", "senders", "rules", "unsubscribe", "settings", "login",
	}
	s.pages = map[string]*template.Template{}
	for _, p := range pages {
		t, err := template.New("layout.html").Funcs(templateFuncs()).
			ParseFS(templatesFS, "templates/layout.html", "templates/"+p+".html")
		if err != nil {
			return err
		}
		s.pages[p] = t
	}
	return nil
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"pct": func(f float64) int { return int(f*100 + 0.5) },
	}
}
