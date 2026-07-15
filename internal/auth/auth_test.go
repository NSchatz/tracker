package auth

import (
	"crypto/sha256"
	"net/http"
	"testing"
)

// TestTokenIsNeverHashLength is the guard behind the guard (roadmap risk path #3).
//
// devices.token_hash is protected by a LENGTH check (32 bytes). That check only stops a raw token
// being stored where its hash belongs IF a raw token can never be 32 bytes. This pins that: a
// token is 43 characters (unpadded base64url of 32 random bytes), so a raw token handed to
// store.CreateDevice fails the length check instead of quietly becoming a stored live credential.
//
// If a future change shortens the token to 32 bytes, this fails HERE, loudly, rather than silently
// re-opening the hole the store's length check was trusted to hold shut.
func TestTokenIsNeverHashLength(t *testing.T) {
	t.Parallel()

	for range 1000 {
		tok, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if len(tok) == sha256.Size {
			t.Fatalf("a token is %d bytes — the same length as a SHA-256 digest, which lets a raw "+
				"token pass the token_hash length check and be stored as a credential", sha256.Size)
		}
	}
	// The digest, by contrast, is always exactly 32 bytes (its array type guarantees it) — which is
	// the length store.CreateDevice checks for. This is what makes the token/hash length gap a real
	// guard: a token can never be 32 bytes, a hash always is.
	var _ [sha256.Size]byte = Token("x").Hash()
}

// TestTokensAreDistinct proves Generate draws real randomness rather than returning a constant —
// two devices sharing a token means either phone can write the other's history.
func TestTokensAreDistinct(t *testing.T) {
	t.Parallel()

	seen := map[Token]struct{}{}
	for range 1000 {
		tok, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("Generate returned a duplicate token %q", tok)
		}
		seen[tok] = struct{}{}
	}
}

// TestHashIsDeterministic: the same token must hash to the same digest, or a device could never
// authenticate against the hash stored at enrollment.
func TestHashIsDeterministic(t *testing.T) {
	t.Parallel()

	tok := Token("a-fixed-synthetic-token-not-a-secret")
	first, second := tok.Hash(), tok.Hash()
	if first != second {
		t.Fatal("Hash is not deterministic — a device could never authenticate against its stored hash")
	}
	if Token("other").Hash() == tok.Hash() {
		t.Fatal("two different tokens hashed to the same digest")
	}
}

func TestFromRequest(t *testing.T) {
	t.Parallel()

	const tok = "the-presented-token"

	t.Run("bearer", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodPost, "/", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		got, ok := FromRequest(r)
		if !ok || string(got) != tok {
			t.Fatalf("FromRequest(Bearer) = %q, %v; want %q, true", got, ok, tok)
		}
	})

	t.Run("bearer scheme is case-insensitive but the token is not", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodPost, "/", nil)
		r.Header.Set("Authorization", "bearer "+tok)
		got, ok := FromRequest(r)
		if !ok || string(got) != tok {
			t.Fatalf("FromRequest(bearer) = %q, %v; want %q, true", got, ok, tok)
		}
	})

	t.Run("basic auth password is the token", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodPost, "/", nil)
		r.SetBasicAuth("device-label-ignored", tok)
		got, ok := FromRequest(r)
		if !ok || string(got) != tok {
			t.Fatalf("FromRequest(Basic) = %q, %v; want %q, true", got, ok, tok)
		}
	})

	rejected := []struct {
		name, header string
	}{
		{"no header", ""},
		{"bearer with empty token", "Bearer "},
		{"bearer with only spaces", "Bearer    "},
		{"basic with empty password", "Basic " + basic("user", "")},
		{"unknown scheme", "Digest something"},
	}
	for _, c := range rejected {
		t.Run("rejected: "+c.name, func(t *testing.T) {
			r, _ := http.NewRequest(http.MethodPost, "/", nil)
			if c.header != "" {
				r.Header.Set("Authorization", c.header)
			}
			if _, ok := FromRequest(r); ok {
				t.Fatalf("FromRequest(%q) accepted a credential it must reject", c.header)
			}
		})
	}
}

// basic renders an HTTP Basic credential the way http.Request.SetBasicAuth would, for the
// empty-password case the helper cannot express.
func basic(user, pass string) string {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.SetBasicAuth(user, pass)
	// Strip the "Basic " scheme prefix the test re-adds.
	return r.Header.Get("Authorization")[len("Basic "):]
}
