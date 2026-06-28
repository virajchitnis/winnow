package digest

import (
	"bytes"
	"fmt"
	"html/template"
	"time"

	"winnow/internal/actions"
	"winnow/internal/store"
)

// NewsletterHighlight is one summarized newsletter (opt-in Phase B section).
type NewsletterHighlight struct {
	Sender, Subject, Summary string
}

// BriefingData is everything the HTML morning briefing renders.
type BriefingData struct {
	Decisions  []store.Decision
	Errors     []store.AppError
	Proposals  []store.SieveCandidate    // pending Sieve rule proposals
	Unsubs     []store.UnsubscribeRecord // unsubscribe candidates needing a decision
	Highlights []NewsletterHighlight     // summarized newsletters (opt-in)
	LLMToday   int
	Since      time.Time
	Now        time.Time
}

// htmlView is the flattened model passed to the template.
type htmlView struct {
	Range                       string
	Total, Moved, Flagged, Kept int
	LowConf                     int
	Important, Review           []briefItem
	PerCategory, TopSenders     []countKV
	Proposals                   []proposalRow
	Unsubs                      []unsubRow
	Highlights                  []NewsletterHighlight
	LLMToday                    int
	Errors                      []errRow
	HasApprovals                bool
}

type briefItem struct{ From, Summary string }
type proposalRow struct {
	Domain, Category string
	Seen             int
}
type unsubRow struct {
	Sender string
	Count  int
}
type errRow struct{ Kind, Message string }

// ComposeHTML builds the briefing subject, an HTML body, and a plain-text
// fallback. Pure and deterministic for testing.
func ComposeHTML(d BriefingData) (subject, htmlBody, textBody string) {
	v := htmlView{LLMToday: d.LLMToday}
	perCat := map[string]int{}
	perSender := map[string]int{}

	for _, dec := range d.Decisions {
		switch dec.Action {
		case string(actions.ActionMoved):
			v.Moved++
			perCat[dec.Category]++
		case string(actions.ActionFlagged):
			v.Flagged++
			v.Important = append(v.Important, toItem(dec))
		default:
			v.Kept++
		}
		if dec.LowConfidence {
			v.LowConf++
			v.Review = append(v.Review, toItem(dec))
		}
		if dec.Sender != "" {
			perSender[dec.Sender]++
		}
	}
	v.Total = len(d.Decisions)
	v.PerCategory = sortedCounts(perCat)
	v.TopSenders = topN(sortedCounts(perSender), 5)

	for _, p := range d.Proposals {
		v.Proposals = append(v.Proposals, proposalRow{Domain: p.Domain, Category: p.Category, Seen: p.Observations})
	}
	for _, u := range d.Unsubs {
		v.Unsubs = append(v.Unsubs, unsubRow{Sender: u.Sender, Count: u.Count})
	}
	v.Highlights = d.Highlights
	v.HasApprovals = len(v.Proposals) > 0 || len(v.Unsubs) > 0
	for _, e := range d.Errors {
		v.Errors = append(v.Errors, errRow{Kind: e.Kind, Message: oneLine(e.Message)})
	}

	v.Range = formatRange(d.Since, d.Now)
	subject = fmt.Sprintf("Winnow briefing — %s", d.Now.Format("Mon Jan 2"))

	var buf bytes.Buffer
	if err := briefingTmpl.Execute(&buf, v); err != nil {
		// Fall back to plain text only if templating ever fails.
		_, t := Compose(d.Decisions, d.Errors, d.Now)
		return subject, "", t
	}
	_, textBody = Compose(d.Decisions, d.Errors, d.Now)
	return subject, buf.String(), textBody
}

func toItem(d store.Decision) briefItem {
	s := d.Summary
	if s == "" {
		s = oneLine(d.Subject)
	}
	from := d.Sender
	if from == "" {
		from = "(unknown sender)"
	}
	return briefItem{From: from, Summary: oneLine(s)}
}

func topN(c []countKV, n int) []countKV {
	if len(c) > n {
		return c[:n]
	}
	return c
}

func formatRange(since, now time.Time) string {
	if since.IsZero() {
		return "last 24 hours"
	}
	return fmt.Sprintf("%s – %s", since.Format("Mon Jan 2, 3:04 PM"), now.Format("Mon Jan 2, 3:04 PM"))
}

// briefingTmpl is a self-contained, inline-styled, ~600px responsive email.
var briefingTmpl = template.Must(template.New("briefing").Funcs(template.FuncMap{
	"dict": func(kv ...any) map[string]any {
		m := make(map[string]any, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			key, _ := kv[i].(string)
			m[key] = kv[i+1]
		}
		return m
	},
}).Parse(`
<!doctype html><html><body style="margin:0;padding:0;background:#f4f1ea;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f4f1ea;padding:24px 12px;">
<tr><td align="center">
<table role="presentation" width="600" cellpadding="0" cellspacing="0" style="max-width:600px;width:100%;background:#ffffff;border-radius:14px;overflow:hidden;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#1c1c1a;border:1px solid rgba(0,0,0,.08);">
  <tr><td style="background:#0f6e56;padding:20px 24px;">
    <span style="font-size:20px;font-weight:700;color:#ffffff;">🌾 Winnow</span>
    <span style="font-size:13px;color:#bfe6d8;float:right;padding-top:5px;">Morning briefing</span>
  </td></tr>
  <tr><td style="padding:18px 24px 4px;">
    <div style="font-size:13px;color:#5f5e5a;">{{.Range}}</div>
    <div style="font-size:22px;font-weight:700;margin-top:2px;">{{.Total}} email{{if ne .Total 1}}s{{end}} handled</div>
  </td></tr>

  <tr><td style="padding:10px 24px;">
    <table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr>
      {{template "stat" dict "n" .Moved "l" "filed"}}
      {{template "stat" dict "n" .Flagged "l" "flagged"}}
      {{template "stat" dict "n" .Kept "l" "kept"}}
      {{template "stat" dict "n" .LowConf "l" "to review"}}
    </tr></table>
  </td></tr>

  {{if .Important}}
  {{template "section" "🔴 Needs your attention"}}
  <tr><td style="padding:0 24px 8px;">
    {{range .Important}}<div style="padding:8px 0;border-bottom:1px solid #efefef;">
      <div style="font-size:14px;font-weight:600;">{{.From}}</div>
      <div style="font-size:13px;color:#5f5e5a;">{{.Summary}}</div>
    </div>{{end}}
  </td></tr>
  {{end}}

  {{if .HasApprovals}}
  {{template "section" "✅ Waiting for your approval"}}
  <tr><td style="padding:0 24px 8px;font-size:13px;color:#3a3a38;">
    {{if .Proposals}}<div style="font-weight:600;margin:6px 0 2px;">Rule proposals</div>
      {{range .Proposals}}<div style="padding:3px 0;">@{{.Domain}} → {{.Category}} <span style="color:#8a8a86;">(seen {{.Seen}})</span></div>{{end}}{{end}}
    {{if .Unsubs}}<div style="font-weight:600;margin:8px 0 2px;">Unsubscribe candidates</div>
      {{range .Unsubs}}<div style="padding:3px 0;">{{.Sender}} <span style="color:#8a8a86;">({{.Count}} seen)</span></div>{{end}}{{end}}
    <div style="margin-top:8px;color:#8a8a86;">Review these in your Winnow dashboard.</div>
  </td></tr>
  {{end}}

  {{if .Highlights}}
  {{template "section" "📰 Newsletter highlights"}}
  <tr><td style="padding:0 24px 8px;">
    {{range .Highlights}}<div style="padding:8px 0;border-bottom:1px solid #efefef;">
      <div style="font-size:14px;font-weight:600;">{{.Subject}}</div>
      <div style="font-size:12px;color:#8a8a86;">{{.Sender}}</div>
      <div style="font-size:13px;color:#3a3a38;margin-top:3px;">{{.Summary}}</div>
    </div>{{end}}
  </td></tr>
  {{end}}

  {{if .PerCategory}}
  {{template "section" "📊 Filed by category"}}
  <tr><td style="padding:0 24px 8px;font-size:13px;">
    {{range .PerCategory}}<div style="padding:3px 0;">{{.Name}} <span style="color:#8a8a86;">· {{.N}}</span></div>{{end}}
  </td></tr>
  {{end}}

  {{if .TopSenders}}
  {{template "section" "📈 Busiest senders"}}
  <tr><td style="padding:0 24px 8px;font-size:13px;">
    {{range .TopSenders}}<div style="padding:3px 0;">{{.Name}} <span style="color:#8a8a86;">· {{.N}}</span></div>{{end}}
  </td></tr>
  {{end}}

  {{if .Review}}
  {{template "section" "🤔 Kept in inbox — worth a look"}}
  <tr><td style="padding:0 24px 8px;">
    {{range .Review}}<div style="padding:6px 0;border-bottom:1px solid #efefef;">
      <div style="font-size:14px;font-weight:600;">{{.From}}</div>
      <div style="font-size:13px;color:#5f5e5a;">{{.Summary}}</div>
    </div>{{end}}
  </td></tr>
  {{end}}

  {{template "section" "💰 Cost & health"}}
  <tr><td style="padding:0 24px 12px;font-size:13px;color:#3a3a38;">
    <div style="padding:3px 0;">{{.LLMToday}} Claude call{{if ne .LLMToday 1}}s{{end}} today</div>
    {{if .Errors}}{{range .Errors}}<div style="padding:3px 0;color:#a32d2d;">⚠ [{{.Kind}}] {{.Message}}</div>{{end}}
    {{else}}<div style="padding:3px 0;color:#0f6e56;">No errors — all systems normal.</div>{{end}}
  </td></tr>

  <tr><td style="padding:14px 24px;background:#faf8f3;border-top:1px solid #efefef;font-size:12px;color:#8a8a86;">
    This briefing is also Winnow's heartbeat — if it stops arriving, check the service is running.
  </td></tr>
</table>
</td></tr></table>
</body></html>

{{define "stat"}}<td width="25%" align="center" style="padding:6px;">
  <div style="font-size:24px;font-weight:700;color:#0f6e56;">{{.n}}</div>
  <div style="font-size:11px;color:#5f5e5a;text-transform:uppercase;letter-spacing:.04em;">{{.l}}</div>
</td>{{end}}

{{define "section"}}<tr><td style="padding:16px 24px 4px;"><div style="font-size:15px;font-weight:700;">{{.}}</div></td></tr>{{end}}
`))
