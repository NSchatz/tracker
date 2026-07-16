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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestServiceAccountKey builds an in-memory service-account JSON (an RSA key + a token endpoint)
// for the token-source tests. The key is generated per test; nothing here is a real credential.
func newTestServiceAccountKey(t *testing.T, tokenURI string) ([]byte, *rsa.PrivateKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal PKCS8: %v", err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	raw, err := json.Marshal(map[string]string{
		"client_email": "tracker@example.iam.gserviceaccount.test",
		"private_key":  string(pemKey),
		"token_uri":    tokenURI,
	})
	if err != nil {
		t.Fatalf("marshal service-account key: %v", err)
	}
	return raw, priv
}

// TestServiceAccountTokenSourceMintsAndCaches drives the production FCM auth path against a stand-in
// token endpoint: it asserts the source signs a correct JWT-bearer assertion (verifiable with the
// service account's own key, with the right issuer/scope/audience), exchanges it for an access token,
// and then CACHES that token rather than re-minting on every call.
func TestServiceAccountTokenSourceMintsAndCaches(t *testing.T) {
	t.Parallel()

	var exchanges atomic.Int64
	var srv *httptest.Server
	var pub *rsa.PublicKey

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
		}
		if g := r.Form.Get("grant_type"); g != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Errorf("grant_type = %q, want the jwt-bearer grant", g)
		}
		assertion := r.Form.Get("assertion")
		verifyAssertion(t, assertion, pub, srv.URL)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"EXAMPLE-fcm-token","expires_in":3600}`))
	}))
	defer srv.Close()

	raw, priv := newTestServiceAccountKey(t, srv.URL)
	pub = &priv.PublicKey

	src, err := newServiceAccountTokenSource(raw, srv.Client())
	if err != nil {
		t.Fatalf("build token source: %v", err)
	}

	got, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "EXAMPLE-fcm-token" {
		t.Errorf("token = %q, want the exchanged access token", got)
	}

	// A second call within the token's lifetime is served from cache — no second exchange.
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token (cached): %v", err)
	}
	if n := exchanges.Load(); n != 1 {
		t.Errorf("token endpoint hit %d times, want 1 (the second call must be cached)", n)
	}

	// Advance past expiry: the source must re-mint.
	src.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token (after expiry): %v", err)
	}
	if n := exchanges.Load(); n != 2 {
		t.Errorf("token endpoint hit %d times, want 2 (expiry must force a re-mint)", n)
	}
}

// verifyAssertion checks the JWT the source signed: three base64url segments, an RS256 signature valid
// under the service account's key, and the OAuth2 claims (issuer, messaging scope, audience).
func verifyAssertion(t *testing.T, assertion string, pub *rsa.PublicKey, tokenURI string) {
	t.Helper()
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion has %d segments, want 3", len(parts))
	}

	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Errorf("assertion signature does not verify under the service-account key: %v", err)
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims struct {
		Iss, Scope, Aud string
		Iat, Exp        int64
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatalf("claims not JSON: %v", err)
	}
	if claims.Iss != "tracker@example.iam.gserviceaccount.test" {
		t.Errorf("iss = %q, want the service-account email", claims.Iss)
	}
	if claims.Scope != fcmMessagingScope {
		t.Errorf("scope = %q, want the firebase.messaging scope", claims.Scope)
	}
	if claims.Aud != tokenURI {
		t.Errorf("aud = %q, want the token endpoint %q", claims.Aud, tokenURI)
	}
	if claims.Exp <= claims.Iat {
		t.Errorf("exp %d is not after iat %d", claims.Exp, claims.Iat)
	}
}

// A malformed key is rejected at construction, not at the first send (the fail-safe).
func TestServiceAccountTokenSourceRejectsBadKey(t *testing.T) {
	t.Parallel()
	if _, err := newServiceAccountTokenSource([]byte(`{"client_email":"x"}`), nil); err == nil {
		t.Fatal("built a token source from a key missing private_key/token_uri, want an error")
	}
	if _, err := newServiceAccountTokenSource([]byte(`not json`), nil); err == nil {
		t.Fatal("built a token source from non-JSON, want an error")
	}
}
