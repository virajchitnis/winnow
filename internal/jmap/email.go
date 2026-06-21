package jmap

import (
	"context"
	"time"
)

// Email is the subset of a JMAP Email object Winnow uses.
type Email struct {
	ID         string          `json:"id"`
	ThreadID   string          `json:"threadId"`
	MailboxIDs map[string]bool `json:"mailboxIds"`
	Keywords   map[string]bool `json:"keywords"`
	From       []EmailAddress  `json:"from"`
	Subject    string          `json:"subject"`
	ReceivedAt time.Time       `json:"receivedAt"`
	Preview    string          `json:"preview"`

	// Header values requested via header:*:asRaw properties (see EmailProperties).
	// Values include the leading space and may include folding whitespace.
	ListUnsubscribe     string `json:"header:List-Unsubscribe:asRaw"`
	ListUnsubscribePost string `json:"header:List-Unsubscribe-Post:asRaw"`
	ListID              string `json:"header:List-Id:asRaw"`
	Precedence          string `json:"header:Precedence:asRaw"`
}

// EmailAddress is a JMAP EmailAddress (name + email).
type EmailAddress struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// SenderEmail returns the first From address, lowercased, or "".
func (e *Email) SenderEmail() string {
	if len(e.From) == 0 {
		return ""
	}
	return lowerASCII(e.From[0].Email)
}

// EmailProperties are the properties Winnow fetches for each email — enough to
// classify and to drive unsubscribe, without pulling full bodies.
var EmailProperties = []string{
	"id", "threadId", "mailboxIds", "keywords", "from", "subject", "receivedAt", "preview",
	"header:List-Unsubscribe:asRaw",
	"header:List-Unsubscribe-Post:asRaw",
	"header:List-Id:asRaw",
	"header:Precedence:asRaw",
}

// QueryInbox returns up to limit email ids currently in the given mailbox,
// newest first.
func (c *Client) QueryInbox(ctx context.Context, mailboxID string, limit int) ([]string, error) {
	sess, err := c.getSession(ctx)
	if err != nil {
		return nil, err
	}
	call, err := newCall("Email/query", map[string]any{
		"accountId":      sess.AccountID(),
		"filter":         map[string]any{"inMailbox": mailboxID},
		"sort":           []map[string]any{{"property": "receivedAt", "isAscending": false}},
		"limit":          limit,
		"calculateTotal": false,
	}, "q")
	if err != nil {
		return nil, err
	}
	resps, err := c.do(ctx, []string{CapCore, CapMail}, call)
	if err != nil {
		return nil, err
	}
	var res struct {
		IDs []string `json:"ids"`
	}
	if err := expect(resps, "q", &res); err != nil {
		return nil, err
	}
	return res.IDs, nil
}

// Changes is the result of an Email/changes call.
type Changes struct {
	OldState  string
	NewState  string
	Created   []string
	Updated   []string
	Destroyed []string
	HasMore   bool
}

// EmailChanges returns the email ids changed since sinceState. If the server
// returns "cannotCalculateChanges", the error satisfies
// IsCannotCalculateChanges and the caller should fall back to QueryInbox.
func (c *Client) EmailChanges(ctx context.Context, sinceState string, maxChanges int) (*Changes, error) {
	sess, err := c.getSession(ctx)
	if err != nil {
		return nil, err
	}
	args := map[string]any{
		"accountId":  sess.AccountID(),
		"sinceState": sinceState,
	}
	if maxChanges > 0 {
		args["maxChanges"] = maxChanges
	}
	call, err := newCall("Email/changes", args, "c")
	if err != nil {
		return nil, err
	}
	resps, err := c.do(ctx, []string{CapCore, CapMail}, call)
	if err != nil {
		return nil, err
	}
	var res struct {
		OldState  string   `json:"oldState"`
		NewState  string   `json:"newState"`
		HasMore   bool     `json:"hasMoreChanges"`
		Created   []string `json:"created"`
		Updated   []string `json:"updated"`
		Destroyed []string `json:"destroyed"`
	}
	if err := expect(resps, "c", &res); err != nil {
		return nil, err
	}
	return &Changes{
		OldState:  res.OldState,
		NewState:  res.NewState,
		Created:   res.Created,
		Updated:   res.Updated,
		Destroyed: res.Destroyed,
		HasMore:   res.HasMore,
	}, nil
}

// MailboxState returns the current Email state token (for seeding Email/changes).
func (c *Client) MailboxState(ctx context.Context) (string, error) {
	sess, err := c.getSession(ctx)
	if err != nil {
		return "", err
	}
	// Email/get with no ids is the cheap way to read the current state token.
	call, err := newCall("Email/get", map[string]any{
		"accountId":  sess.AccountID(),
		"ids":        []string{},
		"properties": []string{"id"},
	}, "s")
	if err != nil {
		return "", err
	}
	resps, err := c.do(ctx, []string{CapCore, CapMail}, call)
	if err != nil {
		return "", err
	}
	var res struct {
		State string `json:"state"`
	}
	if err := expect(resps, "s", &res); err != nil {
		return "", err
	}
	return res.State, nil
}

// GetEmails fetches the given emails with EmailProperties.
func (c *Client) GetEmails(ctx context.Context, ids []string) ([]Email, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	sess, err := c.getSession(ctx)
	if err != nil {
		return nil, err
	}
	call, err := newCall("Email/get", map[string]any{
		"accountId":  sess.AccountID(),
		"ids":        ids,
		"properties": EmailProperties,
	}, "g")
	if err != nil {
		return nil, err
	}
	resps, err := c.do(ctx, []string{CapCore, CapMail}, call)
	if err != nil {
		return nil, err
	}
	var res struct {
		List []Email `json:"list"`
	}
	if err := expect(resps, "g", &res); err != nil {
		return nil, err
	}
	return res.List, nil
}

// EmailUpdate describes a single Email/set patch for one email.
type EmailUpdate struct {
	ID string
	// MailboxIDs, when non-nil, fully replaces the email's mailbox membership.
	MailboxIDs map[string]bool
	// SetKeywords sets specific keywords true/false (partial patch via
	// keywords/<kw> pointers), leaving others untouched.
	SetKeywords map[string]bool
}

// UpdateEmails applies the given patches in a single Email/set call. It returns
// the set of ids the server reported as not updated, mapped to the reason.
func (c *Client) UpdateEmails(ctx context.Context, updates []EmailUpdate) (map[string]string, error) {
	if len(updates) == 0 {
		return nil, nil
	}
	sess, err := c.getSession(ctx)
	if err != nil {
		return nil, err
	}
	update := make(map[string]any, len(updates))
	for _, u := range updates {
		patch := map[string]any{}
		if u.MailboxIDs != nil {
			patch["mailboxIds"] = u.MailboxIDs
		}
		for kw, on := range u.SetKeywords {
			// JSON Pointer patch: keywords/<keyword> = true|null.
			if on {
				patch["keywords/"+kw] = true
			} else {
				patch["keywords/"+kw] = nil
			}
		}
		update[u.ID] = patch
	}
	call, err := newCall("Email/set", map[string]any{
		"accountId": sess.AccountID(),
		"update":    update,
	}, "u")
	if err != nil {
		return nil, err
	}
	resps, err := c.do(ctx, []string{CapCore, CapMail}, call)
	if err != nil {
		return nil, err
	}
	var res struct {
		NotUpdated map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"notUpdated"`
	}
	if err := expect(resps, "u", &res); err != nil {
		return nil, err
	}
	if len(res.NotUpdated) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(res.NotUpdated))
	for id, e := range res.NotUpdated {
		out[id] = e.Type + " " + e.Description
	}
	return out, nil
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
