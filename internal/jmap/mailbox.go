package jmap

import (
	"context"
	"fmt"
	"strings"
)

// Mailbox is the subset of a JMAP Mailbox object Winnow uses.
type Mailbox struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	ParentID string `json:"parentId"`
}

// Mailboxes fetches all mailboxes for the account.
func (c *Client) Mailboxes(ctx context.Context) ([]Mailbox, error) {
	sess, err := c.getSession(ctx)
	if err != nil {
		return nil, err
	}
	call, err := newCall("Mailbox/get", map[string]any{
		"accountId":  sess.AccountID(),
		"ids":        nil,
		"properties": []string{"id", "name", "role", "parentId"},
	}, "mbget")
	if err != nil {
		return nil, err
	}
	resps, err := c.do(ctx, []string{CapCore, CapMail}, call)
	if err != nil {
		return nil, err
	}
	var res struct {
		List []Mailbox `json:"list"`
	}
	if err := expect(resps, "mbget", &res); err != nil {
		return nil, err
	}
	return res.List, nil
}

// MailboxByRole returns the mailbox with the given role (e.g. "inbox"), or false.
func (c *Client) MailboxByRole(ctx context.Context, role string) (Mailbox, bool, error) {
	boxes, err := c.Mailboxes(ctx)
	if err != nil {
		return Mailbox{}, false, err
	}
	for _, m := range boxes {
		if m.Role == role {
			return m, true, nil
		}
	}
	return Mailbox{}, false, nil
}

// EnsureMailbox returns the id of a top-level mailbox with the given name,
// creating it if absent. Name matching is case-insensitive on the exact name.
func (c *Client) EnsureMailbox(ctx context.Context, name string) (string, error) {
	boxes, err := c.Mailboxes(ctx)
	if err != nil {
		return "", err
	}
	for _, m := range boxes {
		if strings.EqualFold(m.Name, name) && m.ParentID == "" {
			return m.ID, nil
		}
	}

	sess, _ := c.getSession(ctx)
	call, err := newCall("Mailbox/set", map[string]any{
		"accountId": sess.AccountID(),
		"create": map[string]any{
			"new": map[string]any{"name": name, "parentId": nil},
		},
	}, "mbset")
	if err != nil {
		return "", err
	}
	resps, err := c.do(ctx, []string{CapCore, CapMail}, call)
	if err != nil {
		return "", err
	}
	var res struct {
		Created    map[string]Mailbox `json:"created"`
		NotCreated map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"notCreated"`
	}
	if err := expect(resps, "mbset", &res); err != nil {
		return "", err
	}
	if m, ok := res.Created["new"]; ok && m.ID != "" {
		return m.ID, nil
	}
	if nc, ok := res.NotCreated["new"]; ok {
		return "", fmt.Errorf("create mailbox %q: %s %s", name, nc.Type, nc.Description)
	}
	return "", fmt.Errorf("create mailbox %q: no id returned", name)
}
