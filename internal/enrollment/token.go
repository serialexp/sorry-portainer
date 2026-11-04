package enrollment

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/serialexp/sorry-portainer/internal/pki"
)

const (
	tokenPrefix   = "spt1"
	tokenIDBytes  = 16
	secretBytes   = 32
	tokenIDLength = 22 // base64url of 16 bytes, unpadded
)

// Token is a parsed enrollment token: spt1.<id>.<secret>.<ca-sha256>, every
// field unpadded base64url. Only ID may be logged.
type Token struct {
	ID     string
	Secret [secretBytes]byte
	CA     pki.Fingerprint
}

var errMalformedToken = errors.New("malformed enrollment token")

// ParseToken decodes a token strictly.
func ParseToken(s string) (Token, error) {
	var t Token
	parts := strings.Split(s, ".")
	if len(parts) != 4 || parts[0] != tokenPrefix || len(parts[1]) != tokenIDLength {
		return t, errMalformedToken
	}
	id, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(id) != tokenIDBytes {
		return t, errMalformedToken
	}
	secret, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(secret) != secretBytes {
		return t, errMalformedToken
	}
	if t.CA, err = pki.ParseFingerprint(parts[3]); err != nil {
		return t, errMalformedToken
	}
	t.ID = parts[1]
	copy(t.Secret[:], secret)
	return t, nil
}

func (t Token) String() string {
	return strings.Join([]string{tokenPrefix, t.ID, base64.RawURLEncoding.EncodeToString(t.Secret[:]), t.CA.String()}, ".")
}

func newToken(ca pki.Fingerprint) (Token, error) {
	t := Token{CA: ca}
	id := make([]byte, tokenIDBytes)
	if _, err := rand.Read(id); err != nil {
		return t, err
	}
	if _, err := rand.Read(t.Secret[:]); err != nil {
		return t, err
	}
	t.ID = base64.RawURLEncoding.EncodeToString(id)
	return t, nil
}

func hashSecret(secret [secretBytes]byte) [sha256.Size]byte { return sha256.Sum256(secret[:]) }
