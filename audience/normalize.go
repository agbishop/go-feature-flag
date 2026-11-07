package audience

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// NormalizeValue applies the normalization of the settings to an id.
func NormalizeValue(value string, s ListSettings) string {
	if s.Normalize == NormalizeTrimLower {
		return strings.ToLower(strings.TrimSpace(value))
	}
	return value
}

// EncodeMember turns an attribute value into the id stored in the audience store:
// normalized first, then hashed (sha256) and encoded (lowercase hex, or the 32 raw bytes).
// The settings are expected to be fully resolved (see Config.Resolve).
func EncodeMember(value string, s ListSettings) string {
	v := NormalizeValue(value, s)
	if s.Hash != HashSHA256 {
		return v
	}
	sum := sha256.Sum256([]byte(v))
	if s.Encoding == EncodingRaw {
		return string(sum[:])
	}
	return hex.EncodeToString(sum[:])
}
