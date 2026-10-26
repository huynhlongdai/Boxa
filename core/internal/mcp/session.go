package mcp

import (
	"crypto/rand"
	"encoding/hex"
)

func newSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
