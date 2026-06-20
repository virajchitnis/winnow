package web

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// cfVerifier verifies a Cloudflare Access JWT.
type cfVerifier interface {
	Verify(ctx context.Context, token string) error
}

// CloudflareAccess verifies Cf-Access-Jwt-Assertion JWTs against a team's JWKS
// and required audience. Keys are fetched from the team's certs endpoint and
// cached.
type CloudflareAccess struct {
	teamDomain string // e.g. myteam.cloudflareaccess.com
	audience   string
	httpClient *http.Client

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

// NewCloudflareAccess constructs a verifier for the given team domain + audience.
func NewCloudflareAccess(teamDomain, audience string) *CloudflareAccess {
	return &CloudflareAccess{
		teamDomain: teamDomain,
		audience:   audience,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		keys:       map[string]*rsa.PublicKey{},
	}
}

const jwksTTL = time.Hour

// Verify validates signature, issuer, audience, and expiry.
func (c *CloudflareAccess) Verify(ctx context.Context, token string) error {
	keyfunc := func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != "RS256" {
			return nil, fmt.Errorf("unexpected alg %q", t.Method.Alg())
		}
		kid, _ := t.Header["kid"].(string)
		key, err := c.keyByID(ctx, kid)
		if err != nil {
			return nil, err
		}
		return key, nil
	}

	parsed, err := jwt.Parse(token, keyfunc,
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer("https://"+c.teamDomain),
		jwt.WithAudience(c.audience),
	)
	if err != nil {
		return authError(err.Error())
	}
	if !parsed.Valid {
		return authError("invalid token")
	}
	return nil
}

func (c *CloudflareAccess) keyByID(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	if key, ok := c.keys[kid]; ok && time.Since(c.fetchedAt) < jwksTTL {
		c.mu.Unlock()
		return key, nil
	}
	c.mu.Unlock()

	if err := c.refresh(ctx); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	key, ok := c.keys[kid]
	if !ok {
		return nil, fmt.Errorf("no JWKS key for kid %q", kid)
	}
	return key, nil
}

func (c *CloudflareAccess) refresh(ctx context.Context) error {
	url := "https://" + c.teamDomain + "/cdn-cgi/access/certs"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch JWKS: status %d", resp.StatusCode)
	}

	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
			Kty string `json:"kty"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("decode JWKS: %w", err)
	}

	keys := map[string]*rsa.PublicKey{}
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pub, err := rsaPublicKey(k.N, k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return fmt.Errorf("JWKS contained no usable RSA keys")
	}

	c.mu.Lock()
	c.keys = keys
	c.fetchedAt = time.Now()
	c.mu.Unlock()
	return nil
}

func rsaPublicKey(nB64, eB64 string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, err
	}
	n := new(big.Int).SetBytes(nBytes)
	e := 0
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}
	if e == 0 {
		return nil, fmt.Errorf("invalid exponent")
	}
	return &rsa.PublicKey{N: n, E: e}, nil
}
