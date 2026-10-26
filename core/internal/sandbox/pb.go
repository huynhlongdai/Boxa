package sandbox

import (
	"encoding/binary"
	"errors"
)

// Minimal protobuf wire encoding for the few boxd.sh messages Boxa needs
// (no generated code, no protobuf dependency).

type pbMsg []byte

func (m pbMsg) key(field, wt int) pbMsg { return binary.AppendUvarint(m, uint64(field<<3|wt)) }

func (m pbMsg) str(field int, s string) pbMsg {
	if s == "" {
		return m
	}
	return m.bytes(field, []byte(s))
}

func (m pbMsg) bytes(field int, b []byte) pbMsg {
	m = m.key(field, 2)
	m = binary.AppendUvarint(m, uint64(len(b)))
	return append(m, b...)
}

func (m pbMsg) uint(field int, v uint64) pbMsg {
	if v == 0 {
		return m
	}
	return binary.AppendUvarint(m.key(field, 0), v)
}

func (m pbMsg) boolean(field int, v bool) pbMsg {
	if !v {
		return m
	}
	return m.uint(field, 1)
}

func (m pbMsg) msg(field int, sub pbMsg) pbMsg { return m.bytes(field, sub) }

// pbFields decodes one message level: field number → values (varints as
// uint64, length-delimited as []byte). Fixed32/64 are skipped.
func pbFields(b []byte) (map[int][]any, error) {
	out := map[int][]any{}
	for len(b) > 0 {
		k, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, errors.New("protobuf: bad key")
		}
		b = b[n:]
		field, wt := int(k>>3), int(k&7)
		switch wt {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, errors.New("protobuf: bad varint")
			}
			b = b[n:]
			out[field] = append(out[field], v)
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return nil, errors.New("protobuf: bad length")
			}
			out[field] = append(out[field], b[n:n+int(l)])
			b = b[n+int(l):]
		case 1:
			if len(b) < 8 {
				return nil, errors.New("protobuf: short fixed64")
			}
			b = b[8:]
		case 5:
			if len(b) < 4 {
				return nil, errors.New("protobuf: short fixed32")
			}
			b = b[4:]
		default:
			return nil, errors.New("protobuf: unsupported wire type")
		}
	}
	return out, nil
}

func pbStr(f map[int][]any, n int) string {
	if v := f[n]; len(v) > 0 {
		if b, ok := v[len(v)-1].([]byte); ok {
			return string(b)
		}
	}
	return ""
}

func pbBytes(f map[int][]any, n int) []byte {
	if v := f[n]; len(v) > 0 {
		if b, ok := v[len(v)-1].([]byte); ok {
			return b
		}
	}
	return nil
}

func pbUint(f map[int][]any, n int) uint64 {
	if v := f[n]; len(v) > 0 {
		if u, ok := v[len(v)-1].(uint64); ok {
			return u
		}
	}
	return 0
}

func pbHas(f map[int][]any, n int) bool { return len(f[n]) > 0 }
