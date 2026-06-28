package jmap

import (
	"context"
	"fmt"
)

// Identity is a JMAP Identity (a from-address the account may send as).
type Identity struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Identities returns the account's sending identities.
func (c *Client) Identities(ctx context.Context) ([]Identity, error) {
	sess, err := c.getSession(ctx)
	if err != nil {
		return nil, err
	}
	call, err := newCall("Identity/get", map[string]any{
		"accountId": sess.SubmissionAccountID(),
	}, "ident")
	if err != nil {
		return nil, err
	}
	resps, err := c.do(ctx, []string{CapCore, CapSubmission}, call)
	if err != nil {
		return nil, err
	}
	var res struct {
		List []Identity `json:"list"`
	}
	if err := expect(resps, "ident", &res); err != nil {
		return nil, err
	}
	return res.List, nil
}

// PrimaryIdentity returns the identity matching the session username, or the
// first identity. The email address is derived here at runtime — never
// hardcoded or committed.
func (c *Client) PrimaryIdentity(ctx context.Context) (Identity, error) {
	sess, err := c.getSession(ctx)
	if err != nil {
		return Identity{}, err
	}
	ids, err := c.Identities(ctx)
	if err != nil {
		return Identity{}, err
	}
	if len(ids) == 0 {
		return Identity{}, fmt.Errorf("jmap: account has no sending identities")
	}
	for _, id := range ids {
		if id.Email != "" && (sess.Username == "" || id.Email == sess.Username) {
			return id, nil
		}
	}
	return ids[0], nil
}

// OutgoingMessage describes a message to send. When HTML is set, it is sent as
// multipart/alternative with Text as the plain-text fallback.
type OutgoingMessage struct {
	FromIdentity Identity
	To           []string
	Subject      string
	Text         string
	HTML         string // optional; when set, sent as multipart/alternative
}

// SendEmail creates a draft, submits it, and (on success) files it in Sent —
// all in one JMAP request using creation-id back-references. Used for the daily
// digest and for mailto: unsubscribe.
func (c *Client) SendEmail(ctx context.Context, msg OutgoingMessage) error {
	sess, err := c.getSession(ctx)
	if err != nil {
		return err
	}
	drafts, hasDrafts, err := c.MailboxByRole(ctx, "drafts")
	if err != nil {
		return err
	}
	sent, _, err := c.MailboxByRole(ctx, "sent")
	if err != nil {
		return err
	}
	if !hasDrafts {
		return fmt.Errorf("jmap: no drafts mailbox found")
	}

	to := make([]EmailAddress, 0, len(msg.To))
	for _, addr := range msg.To {
		to = append(to, EmailAddress{Email: addr})
	}

	// Plain text by default; multipart/alternative when an HTML part is given.
	bodyStructure := map[string]any{"type": "text/plain", "partId": "body"}
	bodyValues := map[string]any{"body": map[string]any{"value": msg.Text}}
	if msg.HTML != "" {
		bodyStructure = map[string]any{
			"type": "multipart/alternative",
			"subParts": []map[string]any{
				{"type": "text/plain", "partId": "text"},
				{"type": "text/html", "partId": "html"},
			},
		}
		bodyValues = map[string]any{
			"text": map[string]any{"value": msg.Text},
			"html": map[string]any{"value": msg.HTML},
		}
	}

	emailCreate := map[string]any{
		"draft": map[string]any{
			"mailboxIds":    map[string]bool{drafts.ID: true},
			"keywords":      map[string]bool{"$draft": true, "$seen": true},
			"from":          []EmailAddress{{Name: msg.FromIdentity.Name, Email: msg.FromIdentity.Email}},
			"to":            to,
			"subject":       msg.Subject,
			"bodyStructure": bodyStructure,
			"bodyValues":    bodyValues,
		},
	}

	onSuccessUpdate := map[string]any{
		"#sub": map[string]any{
			"keywords/$draft": nil,
		},
	}
	if sent.ID != "" {
		onSuccessUpdate["#sub"].(map[string]any)["mailboxIds"] = map[string]bool{sent.ID: true}
	}

	submissionSet := map[string]any{
		"accountId": sess.SubmissionAccountID(),
		"create": map[string]any{
			"sub": map[string]any{
				"identityId": msg.FromIdentity.ID,
				"emailId":    "#draft",
				"envelope": map[string]any{
					"mailFrom": map[string]any{"email": msg.FromIdentity.Email},
					"rcptTo":   rcptTo(msg.To),
				},
			},
		},
		"onSuccessUpdateEmail": onSuccessUpdate,
	}

	emailSet, err := newCall("Email/set", map[string]any{
		"accountId": sess.AccountID(),
		"create":    emailCreate,
	}, "es")
	if err != nil {
		return err
	}
	subSet, err := newCall("EmailSubmission/set", submissionSet, "ss")
	if err != nil {
		return err
	}

	resps, err := c.do(ctx, []string{CapCore, CapMail, CapSubmission}, emailSet, subSet)
	if err != nil {
		return err
	}

	var emailRes struct {
		NotCreated map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"notCreated"`
	}
	if err := expect(resps, "es", &emailRes); err != nil {
		return fmt.Errorf("create draft: %w", err)
	}
	if e, ok := emailRes.NotCreated["draft"]; ok {
		return fmt.Errorf("create draft: %s %s", e.Type, e.Description)
	}

	var subRes struct {
		NotCreated map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"notCreated"`
	}
	if err := expect(resps, "ss", &subRes); err != nil {
		return fmt.Errorf("submit: %w", err)
	}
	if e, ok := subRes.NotCreated["sub"]; ok {
		return fmt.Errorf("submit: %s %s", e.Type, e.Description)
	}
	return nil
}

func rcptTo(addrs []string) []map[string]any {
	out := make([]map[string]any, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, map[string]any{"email": a})
	}
	return out
}
