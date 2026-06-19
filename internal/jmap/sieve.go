package jmap

import (
	"context"
	"fmt"
)

// SieveScript is a JMAP SieveScript object (RFC 9661).
type SieveScript struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IsActive bool   `json:"isActive"`
}

// ActiveSieveScript returns the active Sieve script's id, name, and body, or
// ok=false if there is no active script. The body is fetched via the script's
// blobId through the download mechanism is avoided — instead we request the
// "content" property which Fastmail returns inline.
func (c *Client) ActiveSieveScript(ctx context.Context) (script SieveScript, body string, ok bool, err error) {
	sess, err := c.getSession(ctx)
	if err != nil {
		return SieveScript{}, "", false, err
	}
	call, err := newCall("SieveScript/get", map[string]any{
		"accountId":  sess.AccountID(),
		"ids":        nil,
		"properties": []string{"id", "name", "isActive", "content"},
	}, "sg")
	if err != nil {
		return SieveScript{}, "", false, err
	}
	resps, err := c.do(ctx, []string{CapCore, CapSieve}, call)
	if err != nil {
		return SieveScript{}, "", false, err
	}
	var res struct {
		List []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			IsActive bool   `json:"isActive"`
			Content  string `json:"content"`
		} `json:"list"`
	}
	if err := expect(resps, "sg", &res); err != nil {
		return SieveScript{}, "", false, err
	}
	for _, s := range res.List {
		if s.IsActive {
			return SieveScript{ID: s.ID, Name: s.Name, IsActive: true}, s.Content, true, nil
		}
	}
	return SieveScript{}, "", false, nil
}

// ValidateSieve checks a script body for syntax/validity without saving it.
// A nil error means the script is valid.
func (c *Client) ValidateSieve(ctx context.Context, content string) error {
	sess, err := c.getSession(ctx)
	if err != nil {
		return err
	}
	call, err := newCall("SieveScript/validate", map[string]any{
		"accountId": sess.AccountID(),
		"content":   content,
	}, "sv")
	if err != nil {
		return err
	}
	resps, err := c.do(ctx, []string{CapCore, CapSieve}, call)
	if err != nil {
		return err
	}
	var res struct {
		Error *struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"error"`
	}
	if err := expect(resps, "sv", &res); err != nil {
		return err
	}
	if res.Error != nil {
		return fmt.Errorf("sieve invalid: %s %s", res.Error.Type, res.Error.Description)
	}
	return nil
}

// PutActiveSieve creates-or-updates the named script with the given content and
// activates it (deactivating any other active script). Returns the script id.
func (c *Client) PutActiveSieve(ctx context.Context, name, content, existingID string) (string, error) {
	sess, err := c.getSession(ctx)
	if err != nil {
		return "", err
	}

	args := map[string]any{
		"accountId": sess.AccountID(),
	}
	if existingID != "" {
		args["update"] = map[string]any{
			existingID: map[string]any{"content": content},
		}
		args["onSuccessActivateScript"] = existingID
	} else {
		args["create"] = map[string]any{
			"new": map[string]any{"name": name, "content": content},
		}
		args["onSuccessActivateScript"] = "#new"
	}

	call, err := newCall("SieveScript/set", args, "ss")
	if err != nil {
		return "", err
	}
	resps, err := c.do(ctx, []string{CapCore, CapSieve}, call)
	if err != nil {
		return "", err
	}
	var res struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
		NotCreated map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"notCreated"`
		NotUpdated map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"notUpdated"`
	}
	if err := expect(resps, "ss", &res); err != nil {
		return "", err
	}
	if existingID != "" {
		if e, ok := res.NotUpdated[existingID]; ok {
			return "", fmt.Errorf("update sieve: %s %s", e.Type, e.Description)
		}
		return existingID, nil
	}
	if cr, ok := res.Created["new"]; ok && cr.ID != "" {
		return cr.ID, nil
	}
	if e, ok := res.NotCreated["new"]; ok {
		return "", fmt.Errorf("create sieve: %s %s", e.Type, e.Description)
	}
	return "", fmt.Errorf("put sieve: no id returned")
}
