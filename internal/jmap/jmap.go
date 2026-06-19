// Package jmap is a small JMAP client for Fastmail covering the methods Winnow
// needs: Mailbox, Email (query/changes/get/set), EmailSubmission, Identity, and
// SieveScript (RFC 9661). It deliberately implements only what the app uses.
//
// The HTTP client is injected so tests can run against httptest servers, and
// the session is fetched lazily and cached.
package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// Capability URIs.
const (
	CapCore       = "urn:ietf:params:jmap:core"
	CapMail       = "urn:ietf:params:jmap:mail"
	CapSubmission = "urn:ietf:params:jmap:submission"
	CapSieve      = "urn:ietf:params:jmap:sieve"
)

// DefaultSessionURL is Fastmail's JMAP session resource.
const DefaultSessionURL = "https://api.fastmail.com/jmap/session"

// Doer is the subset of *http.Client the package needs (injectable for tests).
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client is a JMAP client bound to a single account/token.
type Client struct {
	doer       Doer
	token      string
	sessionURL string

	mu      sync.Mutex
	session *Session
}

// Option configures a Client.
type Option func(*Client)

// WithDoer overrides the HTTP client (used in tests).
func WithDoer(d Doer) Option { return func(c *Client) { c.doer = d } }

// WithSessionURL overrides the session resource URL (used in tests).
func WithSessionURL(u string) Option { return func(c *Client) { c.sessionURL = u } }

// New returns a Client authenticating with the given bearer token.
func New(token string, opts ...Option) *Client {
	c := &Client{
		doer:       http.DefaultClient,
		token:      token,
		sessionURL: DefaultSessionURL,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Session is the relevant subset of the JMAP session resource.
type Session struct {
	APIURL          string            `json:"apiUrl"`
	Username        string            `json:"username"`
	PrimaryAccounts map[string]string `json:"primaryAccounts"`
	rawCapabilities map[string]json.RawMessage
}

// AccountID returns the primary account id for a capability (mail by default).
func (s *Session) AccountID() string { return s.PrimaryAccounts[CapMail] }

// SubmissionAccountID returns the account id used for EmailSubmission.
func (s *Session) SubmissionAccountID() string {
	if id := s.PrimaryAccounts[CapSubmission]; id != "" {
		return id
	}
	return s.AccountID()
}

// HasCapability reports whether the server advertises a capability.
func (s *Session) HasCapability(uri string) bool {
	_, ok := s.rawCapabilities[uri]
	return ok
}

// getSession fetches and caches the session resource.
func (c *Client) getSession(ctx context.Context) (*Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		return c.session, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.sessionURL, nil)
	if err != nil {
		return nil, err
	}
	c.authorize(req)

	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jmap session: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	var raw struct {
		APIURL          string                     `json:"apiUrl"`
		Username        string                     `json:"username"`
		PrimaryAccounts map[string]string          `json:"primaryAccounts"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("jmap session decode: %w", err)
	}
	if raw.APIURL == "" {
		return nil, fmt.Errorf("jmap session: empty apiUrl")
	}
	c.session = &Session{
		APIURL:          raw.APIURL,
		Username:        raw.Username,
		PrimaryAccounts: raw.PrimaryAccounts,
		rawCapabilities: raw.Capabilities,
	}
	return c.session, nil
}

// Session returns the (cached) session resource.
func (c *Client) Session(ctx context.Context) (*Session, error) { return c.getSession(ctx) }

func (c *Client) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
}

// invocation is one method call/response triple: [name, args, callId].
type invocation struct {
	Name string
	Args json.RawMessage
	ID   string
}

func (iv invocation) MarshalJSON() ([]byte, error) {
	return json.Marshal([3]json.RawMessage{
		jsonString(iv.Name),
		iv.Args,
		jsonString(iv.ID),
	})
}

func (iv *invocation) UnmarshalJSON(b []byte) error {
	var triple [3]json.RawMessage
	if err := json.Unmarshal(b, &triple); err != nil {
		return err
	}
	if err := json.Unmarshal(triple[0], &iv.Name); err != nil {
		return err
	}
	iv.Args = triple[1]
	return json.Unmarshal(triple[2], &iv.ID)
}

type apiRequest struct {
	Using       []string     `json:"using"`
	MethodCalls []invocation `json:"methodCalls"`
}

type apiResponse struct {
	MethodResponses []invocation `json:"methodResponses"`
	SessionState    string       `json:"sessionState"`
}

// do sends a batch of method calls and returns the response invocations.
func (c *Client) do(ctx context.Context, using []string, calls ...invocation) ([]invocation, error) {
	sess, err := c.getSession(ctx)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(apiRequest{Using: using, MethodCalls: calls})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sess.APIURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	c.authorize(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jmap request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	var out apiResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("jmap response decode: %w", err)
	}
	return out.MethodResponses, nil
}

// newCall builds an invocation, marshaling args.
func newCall(name string, args any, id string) (invocation, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return invocation{}, err
	}
	return invocation{Name: name, Args: raw, ID: id}, nil
}

// expect finds the response invocation with the given call id and decodes its
// args into v. A JMAP method-level error ("error" response) is returned as
// *MethodError.
func expect(resps []invocation, id string, v any) error {
	for _, r := range resps {
		if r.ID != id {
			continue
		}
		if r.Name == "error" {
			var me MethodError
			_ = json.Unmarshal(r.Args, &me)
			return &me
		}
		if v == nil {
			return nil
		}
		return json.Unmarshal(r.Args, v)
	}
	return fmt.Errorf("jmap: no response for call %q", id)
}

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}
