package web

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestCloudflareAccessVerify(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const kid = "test-kid"
	const team = "myteam.cloudflareaccess.com"
	const aud = "aud-123"

	// Serve a JWKS containing the public key.
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kid": kid, "kty": "RSA",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}},
		})
	}))
	defer jwksSrv.Close()

	cf := NewCloudflareAccess(team, aud)
	cf.certsURL = jwksSrv.URL

	makeToken := func(audience string, exp time.Time) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": "https://" + team,
			"aud": audience,
			"exp": exp.Unix(),
		})
		tok.Header["kid"] = kid
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	// Valid token.
	if err := cf.Verify(context.Background(), makeToken(aud, time.Now().Add(time.Hour))); err != nil {
		t.Errorf("valid token rejected: %v", err)
	}
	// Wrong audience.
	if err := cf.Verify(context.Background(), makeToken("other", time.Now().Add(time.Hour))); err == nil {
		t.Error("wrong audience should be rejected")
	}
	// Expired.
	if err := cf.Verify(context.Background(), makeToken(aud, time.Now().Add(-time.Hour))); err == nil {
		t.Error("expired token should be rejected")
	}
	// Garbage.
	if err := cf.Verify(context.Background(), "not.a.jwt"); err == nil {
		t.Error("garbage token should be rejected")
	}
}

func TestRSAPublicKeyParsing(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	pub, err := rsaPublicKey(n, e)
	if err != nil {
		t.Fatal(err)
	}
	if pub.N.Cmp(key.N) != 0 || pub.E != key.E {
		t.Error("parsed key mismatch")
	}
}
