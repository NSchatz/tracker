// Package auth issues and verifies tracker's per-device bearer tokens.
//
// # Why a token is not its hash, and why that distinction is load-bearing
//
// A device authenticates with a bearer token — an opaque, high-entropy string it presents on
// every write. The server never stores that string: it stores the SHA-256 of it (see
// store.CreateDevice, token_hash). A database that is read (a backup, a dump, a compromised
// replica) must not hand the reader the ability to impersonate a family's phone, and storing
// only the digest is what buys that — the digest cannot be replayed as a token.
//
// So there are two byte strings that must never be confused: the TOKEN (what the client sends)
// and its HASH (what the database holds). This package owns the token; store owns the hash. The
// one place they meet is Token.Hash, and the flow is always the same: generate a token, store its
// hash, and on every request hash the presented token and look the device up by that hash.
//
// # The length guard, made real (S2, roadmap risk path #3)
//
// devices.token_hash is guarded by a LENGTH check (octet_length = 32), not a "this is really a
// digest" check — no such check exists, because any 32 bytes are a syntactically valid SHA-256.
// The failure that guard is meant to stop is a caller accidentally storing the RAW TOKEN where the
// hash belongs, turning the table into a file of live credentials. That guard is only sufficient
// if a raw token can never itself be 32 bytes long — otherwise it would sail straight through.
//
// A token here is 32 random bytes rendered as unpadded base64url: 43 characters, never 32. That is
// not incidental; TestTokenIsNeverHashLength pins it, so the length guard in store is a genuine
// barrier against a raw token masquerading as a digest rather than a coincidence waiting to break.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
)

// TokenBytes is the amount of cryptographic randomness in a token before encoding. 32 bytes = 256
// bits, comfortably past any brute-force reach, and — encoded — never collides with the 32-BYTE
// length of a SHA-256 digest (see the package doc).
const TokenBytes = 32

// Token is a device's bearer credential in its plaintext form: the string a client sends and the
// server hashes. It is a secret. It is returned exactly once, at enrollment, and never stored.
type Token string

// Generate mints a new token from the system CSPRNG.
//
// base64url without padding keeps the token safe to carry in an Authorization header, a URL, or an
// HTTP Basic password without escaping — and, deliberately, 43 characters long, so it can never be
// mistaken (by length) for the 32-byte digest it will be hashed into.
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
//   - Bearer — the first-party client and any modern API caller: `Authorization: Bearer <token>`.
//   - Basic — the stock OwnTracks app (roadmap §5.2, interim adapter), which authenticates with an
//     HTTP Basic username/password. The token is the PASSWORD; the username is the device's own
//     label and is ignored, because the device's identity comes from the token, never from a field
//     the client can set freely.
//
// It returns ok=false for a missing, empty, or malformed credential, so the caller answers 401
// without ever reaching the database. A blank token is treated as absent: an empty string that
// hashed to a fixed digest and then happened to match a row would be an authentication bypass.
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
