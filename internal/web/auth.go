package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "winnow_session"
	sessionTTL    = 7 * 24 * time.Hour
)

// checkPassword verifies a password against the active bcrypt hash (a
// dashboard-set hash overrides the env default).
func (s *Server) checkPassword(pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(s.currentHash()), []byte(pw)) == nil
}

// issueSession sets a signed session cookie valid for sessionTTL.
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request) {
	exp := time.Now().Add(sessionTTL).Unix()
	value := signSession(s.sessionSecret, exp)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil || forwardedHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(exp, 0),
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

// clearSession expires the session cookie.
func clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
	})
}

// hasValidSession reports whether the request carries a valid, unexpired
// session cookie.
func (s *Server) hasValidSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return validSession(s.sessionSecret, c.Value)
}

// signSession returns "exp.signature" where signature is HMAC-SHA256(exp).
func signSession(secret string, exp int64) string {
	payload := strconv.FormatInt(exp, 10)
	return payload + "." + sign(secret, payload)
}

func validSession(secret, value string) bool {
	dot := strings.LastIndexByte(value, '.')
	if dot < 0 {
		return false
	}
	payload, sig := value[:dot], value[dot+1:]
	expected := sign(secret, payload)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return false
	}
	exp, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < exp
}

func sign(secret, payload string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// requireAuth wraps a handler, enforcing (1) Cloudflare Access JWT verification
// for tunnel requests when configured, and (2) a valid app session — redirecting
// to the login page otherwise.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.verifyCloudflareAccess(r); err != nil {
			http.Error(w, "forbidden: "+err.Error(), http.StatusForbidden)
			return
		}
		if !s.hasValidSession(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// verifyCloudflareAccess validates the Cf-Access-Jwt-Assertion header when CF
// Access is configured AND the request arrived via the tunnel (i.e. the header
// is present). Requests on the Tailnet have no such header and pass this layer
// (they are still gated by the app session and the Tailnet itself).
func (s *Server) verifyCloudflareAccess(r *http.Request) error {
	if s.cfVerifier == nil {
		return nil // CF Access not configured
	}
	token := r.Header.Get("Cf-Access-Jwt-Assertion")
	if token == "" {
		// No tunnel header: must be a Tailnet request. Allow (session still
		// required). If you want to force all access through the tunnel, front
		// the app with the tunnel only.
		return nil
	}
	return s.cfVerifier.Verify(r.Context(), token)
}

func forwardedHTTPS(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// authError is a small helper for consistent messages.
func authError(reason string) error { return fmt.Errorf("cloudflare access: %s", reason) }
