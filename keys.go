package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

const keyPrefix = "llmgw_"

// NewKey returns a fresh client key: 32 random bytes, base64url, with a prefix that makes a
// leaked key recognisable in a log or a paste.
func NewKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return keyPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// HashKey is what the configuration stores. A 256-bit random key needs no slow hash: there is
// nothing to guess that is cheaper than the key itself.
func HashKey(key string) string {
	s := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(s[:])
}

// bearer returns the key in "Authorization: Bearer <key>", or "" when there is none.
func bearer(h string) string {
	scheme, key, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(key)
}

// clientForKey compares the presented key's hash with every client's in constant time and
// looks at all of them whatever matches, so the time taken says nothing about which client
// or how much of a hash matched.
func clientForKey(clients []*Client, key string) *Client {
	if key == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(key))
	var found *Client
	for _, c := range clients {
		stored, err := hex.DecodeString(strings.TrimPrefix(c.KeyHash, "sha256:"))
		if err != nil || len(stored) != len(sum) {
			continue
		}
		if subtle.ConstantTimeCompare(sum[:], stored) == 1 && found == nil {
			found = c
		}
	}
	return found
}
