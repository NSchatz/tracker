// Package auth issues and verifies tracker's per-device bearer tokens.
//
// Two byte strings must never be confused: the TOKEN (what the client sends) and its HASH (what the
// database holds). The server never stores the token, only its SHA-256, so a database that is read -
// a backup, a dump, a compromised replica - cannot hand the reader the ability to impersonate a
// family's phone. This package owns the token, store owns the hash, and Token.Hash is the one place
// they meet.
//
// devices.token_hash is guarded by a LENGTH check (octet_length = 32), because no check for "this is
// really a digest" exists: any 32 bytes are a syntactically valid SHA-256. That guard stops a caller
// storing the RAW TOKEN where the hash belongs, and it is only sufficient while a raw token can never
// itself be 32 bytes long. A token here is 32 random bytes as unpadded base64url - 43 characters,
// never 32 - and TestTokenIsNeverHashLength pins it, so the guard is a barrier rather than a
// coincidence waiting to break.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
)

// TokenBytes is the cryptographic randomness in a token before encoding: 256 bits, and encoded it
// never collides with the 32-BYTE length of a SHA-256 digest.
const TokenBytes = 32

// Token is a device's bearer credential in its plaintext form: the string a client sends and the
// server hashes. It is a secret. It is returned exactly once, at enrollment, and never stored.
type Token string

// Generate mints a new token from the system CSPRNG. base64url without padding carries safely in an
// Authorization header, a URL or an HTTP Basic password without escaping, and is 43 characters, so it
// can never be mistaken by length for the 32-byte digest it will be hashed into.
func Generate() (Token, error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return Token(base64.RawURLEncoding.EncodeToString(b)), nil
}

// Hash returns the SHA-256 digest stored in devices.token_hash. This is the ONLY thing about a
// token that ever reaches the database.
func (t Token) Hash() [sha256.Size]byte {
	return sha256.Sum256([]byte(t))
}

// FromRequest extracts the presented token from the Authorization header, accepting both schemes
// tracker's clients use:
//
//   - Bearer - the first-party client and any modern API caller.
//   - Basic - the stock OwnTracks app (roadmap §5.2, interim adapter). The token is the PASSWORD; the
//     username is ignored, because a device's identity comes from the token, never from a field the
//     client can set freely.
//
// It returns ok=false for a missing, empty or malformed credential, so the caller answers 401 without
// reaching the database. A blank token is absent: an empty string that hashed to a fixed digest and
// then matched a row would be an authentication bypass.
func FromRequest(r *http.Request) (Token, bool) {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return "", false
	}

	if rest, ok := cutPrefixFold(h, "Bearer "); ok {
		t := strings.TrimSpace(rest)
		if t == "" {
			return "", false
		}
		return Token(t), true
	}

	// Basic auth: the token is the password. Go's BasicAuth parses the base64 credential for us.
	if _, pass, ok := r.BasicAuth(); ok {
		if pass == "" {
			return "", false
		}
		return Token(pass), true
	}

	return "", false
}

// cutPrefixFold is strings.CutPrefix with an ASCII-case-insensitive match, because the auth scheme
// name ("Bearer") is case-insensitive per RFC 7235 while the token that follows is not.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}
