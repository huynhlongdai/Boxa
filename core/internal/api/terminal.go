package api

import (
	"strings"
	"unicode/utf8"
)

// utf8Splitter holds back an incomplete trailing UTF-8 sequence so every
// text frame we send is valid UTF-8 (browsers close the socket otherwise).
type utf8Splitter struct{ pending []byte }

func (u *utf8Splitter) feed(b []byte) string {
	buf := append(u.pending, b...)
	cut := len(buf)
	for k := 1; k <= 3 && k <= len(buf); k++ {
		c := buf[len(buf)-k]
		if c&0xC0 != 0x80 { // ASCII or lead byte
			if c >= 0xC0 && !utf8.FullRune(buf[len(buf)-k:]) {
				cut = len(buf) - k
			}
			break
		}
	}
	u.pending = append([]byte(nil), buf[cut:]...)
	return strings.ToValidUTF8(string(buf[:cut]), "\uFFFD")
}

