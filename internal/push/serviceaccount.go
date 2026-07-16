package push

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ServiceAccountTokenSource mints FCM HTTP v1 access tokens from a Google service-account key, using
// the JWT-bearer OAuth2 grant ([oauth2 service-account]): sign a short-lived JWT asserting the
// service account and the messaging scope, exchange it at the token endpoint for an access token, and
// cache that token until just before it expires.
//
// This is the PRODUCTION auth path — the piece the roadmap's owner-side "manual real-device check"
// exercises, distinct from the mock-FCM unit tests that use a StaticTokenSource. It is stdlib-only
// (crypto/rsa RS256), so it adds no dependency; the JWT assertion it builds and the exchange it
// performs are both unit-tested (serviceaccount_test.go) against a generated key and an httptest
// token server, so it is not untested surface even though the real Google endpoint is not reached in
// CI.
type ServiceAccountTokenSource struct {
	email      string
	privateKey *rsa.PrivateKey
	tokenURI   string
	scope      string
	client     *http.Client
	now        func() time.Time // injectable clock so the cache/expiry logic is testable

	mu     sync.Mutex
	cached string
	expiry time.Time
}

// serviceAccountKey is the subset of a Google service-account JSON file this needs: who is signing,
// the private key to sign with, and where to exchange the assertion.
type serviceAccountKey struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

// NewServiceAccountTokenSourceFromFile reads a service-account JSON key from disk and builds a token
// source for FCM messaging. It fails loudly on a missing or malformed key — a push backend that
// cannot authenticate should be discovered at start-up, not at the first crossing (§ fail-safe).
func NewServiceAccountTokenSourceFromFile(path string, client *http.Client) (*ServiceAccountTokenSource, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read service-account key %q: %w", path, err)
	}
	return newServiceAccountTokenSource(raw, client)
}

// newServiceAccountTokenSource parses the key bytes and constructs the source. Split out from the file
// read so a test can drive it from an in-memory key without touching disk.
func newServiceAccountTokenSource(raw []byte, client *http.Client) (*ServiceAccountTokenSource, error) {
	var key serviceAccountKey
	if err := json.Unmarshal(raw, &key); err != nil {
		return nil, fmt.Errorf("parse service-account key: %w", err)
	}
	if key.ClientEmail == "" || key.PrivateKey == "" || key.TokenURI == "" {
		return nil, fmt.Errorf("service-account key is missing client_email, private_key, or token_uri")
	}
	pk, err := parseRSAPrivateKey(key.PrivateKey)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &ServiceAccountTokenSource{
		email:      key.ClientEmail,
		privateKey: pk,
		tokenURI:   key.TokenURI,
		scope:      fcmMessagingScope,
		client:     client,
		now:        time.Now,
	}, nil
}

// parseRSAPrivateKey decodes the PEM private key from a service-account file. Google issues PKCS#8
// ("BEGIN PRIVATE KEY"); PKCS#1 ("BEGIN RSA PRIVATE KEY") is accepted too so a re-encoded key still
// works.
func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("service-account private_key is not PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse service-account private key: %w", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("service-account private key is %T, not RSA", parsed)
	}
	return rsaKey, nil
}

// tokenExpiryGrace is how long before a token's real expiry the cache treats it as stale, so a token
// never expires mid-flight between the check and the FCM call that uses it.
const tokenExpiryGrace = 60 * time.Second

// Token returns a cached access token if one is still fresh, otherwise mints a new one. It is safe for
// concurrent use — the dispatcher worker is single-threaded today, but the lock costs nothing and
// makes the source correct if that ever changes.
func (s *ServiceAccountTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cached != "" && s.now().Before(s.expiry.Add(-tokenExpiryGrace)) {
		return s.cached, nil
	}

	assertion, err := s.signAssertion(s.now())
	if err != nil {
		return "", err
	}
	tok, ttl, err := s.exchange(ctx, assertion)
	if err != nil {
		return "", err
	}
	s.cached = tok
	s.expiry = s.now().Add(ttl)
	return tok, nil
}

// jwtHeader and jwtClaims are the two JSON objects a signed JWT is built from. The claims are the
// OAuth2 JWT-bearer assertion: the service account (iss/sub), the token endpoint (aud), the requested
// scope, and a short validity window.
var jwtHeader = map[string]string{"alg": "RS256", "typ": "JWT"}

type jwtClaims struct {
	Iss   string `json:"iss"`
	Scope string `json:"scope"`
	Aud   string `json:"aud"`
	Iat   int64  `json:"iat"`
	Exp   int64  `json:"exp"`
}

// jwtAssertionTTL is how long the signed assertion is valid. One hour is Google's maximum and is
// irrelevant to how long the RESULTING access token lasts — the assertion is spent immediately at the
// exchange.
const jwtAssertionTTL = time.Hour

// signAssertion builds and RS256-signs the JWT-bearer assertion for the given instant. It is separated
// from the exchange so a test can assert the header/claims/signature directly.
func (s *ServiceAccountTokenSource) signAssertion(now time.Time) (string, error) {
	header, err := json.Marshal(jwtHeader)
	if err != nil {
		return "", fmt.Errorf("marshal jwt header: %w", err)
	}
	claims, err := json.Marshal(jwtClaims{
		Iss:   s.email,
		Scope: s.scope,
		Aud:   s.tokenURI,
		Iat:   now.Unix(),
		Exp:   now.Add(jwtAssertionTTL).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("marshal jwt claims: %w", err)
	}

	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(claims)

	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// tokenResponse is the OAuth2 token endpoint's success body.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
}

// exchange POSTs the JWT-bearer assertion to the token endpoint and returns the access token and its
// lifetime. A non-2xx or a body without an access token is an error — the caller (Token) surfaces it,
// and the dispatcher treats a token failure exactly like a send failure: retried, then logged, never
// crashed.
func (s *ServiceAccountTokenSource) exchange(ctx context.Context, assertion string) (string, time.Duration, error) {
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("exchange jwt for access token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", 0, fmt.Errorf("decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", 0, fmt.Errorf("token endpoint returned no access_token")
	}
	return tr.AccessToken, time.Duration(tr.ExpiresIn) * time.Second, nil
}
