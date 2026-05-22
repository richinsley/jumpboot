package wasmguest

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Minimal, stdlib-only MessagePack codec for the jumpboot queue protocol.
// It implements the subset the protocol uses — nil, bool, int, float, str,
// bin, array, map — and interoperates with the Go host's
// github.com/vmihailenco/msgpack/v5. Decoded integers are returned as int64,
// floats as float64, strings as string, maps as map[string]any.

// ---- encode ---------------------------------------------------------------

func mpEncode(v any) []byte {
	var out []byte
	return mpEncodeValue(out, v)
}

func mpEncodeValue(out []byte, v any) []byte {
	switch x := v.(type) {
	case nil:
		return append(out, 0xc0)
	case bool:
		if x {
			return append(out, 0xc3)
		}
		return append(out, 0xc2)
	case int:
		return mpEncodeInt(out, int64(x))
	case int8:
		return mpEncodeInt(out, int64(x))
	case int16:
		return mpEncodeInt(out, int64(x))
	case int32:
		return mpEncodeInt(out, int64(x))
	case int64:
		return mpEncodeInt(out, x)
	case uint:
		return mpEncodeUint(out, uint64(x))
	case uint8:
		return mpEncodeUint(out, uint64(x))
	case uint16:
		return mpEncodeUint(out, uint64(x))
	case uint32:
		return mpEncodeUint(out, uint64(x))
	case uint64:
		return mpEncodeUint(out, x)
	case float32:
		return mpEncodeFloat(out, float64(x))
	case float64:
		return mpEncodeFloat(out, x)
	case string:
		return mpEncodeString(out, x)
	case []byte:
		return mpEncodeBinary(out, x)
	case []any:
		return mpEncodeArray(out, x)
	case map[string]any:
		return mpEncodeMap(out, x)
	default:
		// Unsupported types are encoded as their fmt.Sprint form so a stray
		// value can never wedge the protocol loop.
		return mpEncodeString(out, fmt.Sprint(x))
	}
}

func mpEncodeInt(out []byte, n int64) []byte {
	if n >= 0 {
		return mpEncodeUint(out, uint64(n))
	}
	switch {
	case n >= -0x20:
		return append(out, byte(n))
	case n >= -0x80:
		return append(out, 0xd0, byte(n))
	case n >= -0x8000:
		var b [2]byte
		binary.BigEndian.PutUint16(b[:], uint16(n))
		return append(out, 0xd1, b[0], b[1])
	case n >= -0x80000000:
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(n))
		return append(append(out, 0xd2), b[:]...)
	default:
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		return append(append(out, 0xd3), b[:]...)
	}
}

func mpEncodeUint(out []byte, n uint64) []byte {
	switch {
	case n < 0x80:
		return append(out, byte(n))
	case n < 0x100:
		return append(out, 0xcc, byte(n))
	case n < 0x10000:
		var b [2]byte
		binary.BigEndian.PutUint16(b[:], uint16(n))
		return append(out, 0xcd, b[0], b[1])
	case n < 0x100000000:
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(n))
		return append(append(out, 0xce), b[:]...)
	default:
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], n)
		return append(append(out, 0xcf), b[:]...)
	}
}

func mpEncodeFloat(out []byte, f float64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], math.Float64bits(f))
	return append(append(out, 0xcb), b[:]...)
}

func mpEncodeString(out []byte, s string) []byte {
	n := len(s)
	switch {
	case n < 0x20:
		out = append(out, 0xa0|byte(n))
	case n < 0x100:
		out = append(out, 0xd9, byte(n))
	case n < 0x10000:
		var b [2]byte
		binary.BigEndian.PutUint16(b[:], uint16(n))
		out = append(out, 0xda, b[0], b[1])
	default:
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(n))
		out = append(append(out, 0xdb), b[:]...)
	}
	return append(out, s...)
}

func mpEncodeBinary(out []byte, data []byte) []byte {
	n := len(data)
	switch {
	case n < 0x100:
		out = append(out, 0xc4, byte(n))
	case n < 0x10000:
		var b [2]byte
		binary.BigEndian.PutUint16(b[:], uint16(n))
		out = append(out, 0xc5, b[0], b[1])
	default:
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(n))
		out = append(append(out, 0xc6), b[:]...)
	}
	return append(out, data...)
}

func mpEncodeArray(out []byte, arr []any) []byte {
	n := len(arr)
	switch {
	case n < 0x10:
		out = append(out, 0x90|byte(n))
	case n < 0x10000:
		var b [2]byte
		binary.BigEndian.PutUint16(b[:], uint16(n))
		out = append(out, 0xdc, b[0], b[1])
	default:
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(n))
		out = append(append(out, 0xdd), b[:]...)
	}
	for _, item := range arr {
		out = mpEncodeValue(out, item)
	}
	return out
}

func mpEncodeMap(out []byte, m map[string]any) []byte {
	n := len(m)
	switch {
	case n < 0x10:
		out = append(out, 0x80|byte(n))
	case n < 0x10000:
		var b [2]byte
		binary.BigEndian.PutUint16(b[:], uint16(n))
		out = append(out, 0xde, b[0], b[1])
	default:
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(n))
		out = append(append(out, 0xdf), b[:]...)
	}
	for k, v := range m {
		out = mpEncodeString(out, k)
		out = mpEncodeValue(out, v)
	}
	return out
}

// ---- decode ---------------------------------------------------------------

type mpReader struct {
	buf []byte
	pos int
}

func mpDecode(data []byte) (v any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("msgpack: malformed input: %v", r)
		}
	}()
	r := &mpReader{buf: data}
	return r.value(), nil
}

func (r *mpReader) byte() byte {
	b := r.buf[r.pos]
	r.pos++
	return b
}

func (r *mpReader) take(n int) []byte {
	s := r.buf[r.pos : r.pos+n]
	r.pos += n
	return s
}

func (r *mpReader) value() any {
	b := r.byte()
	switch {
	case b <= 0x7f:
		return int64(b)
	case b >= 0xe0:
		return int64(int8(b))
	case b >= 0x80 && b <= 0x8f:
		return r.mapOf(int(b & 0x0f))
	case b >= 0x90 && b <= 0x9f:
		return r.arrayOf(int(b & 0x0f))
	case b >= 0xa0 && b <= 0xbf:
		return string(r.take(int(b & 0x1f)))
	}
	switch b {
	case 0xc0:
		return nil
	case 0xc2:
		return false
	case 0xc3:
		return true
	case 0xc4:
		return append([]byte(nil), r.take(int(r.byte()))...)
	case 0xc5:
		return append([]byte(nil), r.take(int(binary.BigEndian.Uint16(r.take(2))))...)
	case 0xc6:
		return append([]byte(nil), r.take(int(binary.BigEndian.Uint32(r.take(4))))...)
	case 0xca:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(r.take(4))))
	case 0xcb:
		return math.Float64frombits(binary.BigEndian.Uint64(r.take(8)))
	case 0xcc:
		return int64(r.byte())
	case 0xcd:
		return int64(binary.BigEndian.Uint16(r.take(2)))
	case 0xce:
		return int64(binary.BigEndian.Uint32(r.take(4)))
	case 0xcf:
		u := binary.BigEndian.Uint64(r.take(8))
		if u <= math.MaxInt64 {
			return int64(u)
		}
		return u
	case 0xd0:
		return int64(int8(r.byte()))
	case 0xd1:
		return int64(int16(binary.BigEndian.Uint16(r.take(2))))
	case 0xd2:
		return int64(int32(binary.BigEndian.Uint32(r.take(4))))
	case 0xd3:
		return int64(binary.BigEndian.Uint64(r.take(8)))
	case 0xd9:
		return string(r.take(int(r.byte())))
	case 0xda:
		return string(r.take(int(binary.BigEndian.Uint16(r.take(2)))))
	case 0xdb:
		return string(r.take(int(binary.BigEndian.Uint32(r.take(4)))))
	case 0xdc:
		return r.arrayOf(int(binary.BigEndian.Uint16(r.take(2))))
	case 0xdd:
		return r.arrayOf(int(binary.BigEndian.Uint32(r.take(4))))
	case 0xde:
		return r.mapOf(int(binary.BigEndian.Uint16(r.take(2))))
	case 0xdf:
		return r.mapOf(int(binary.BigEndian.Uint32(r.take(4))))
	default:
		panic(fmt.Sprintf("unsupported type byte 0x%x", b))
	}
}

func (r *mpReader) arrayOf(n int) []any {
	arr := make([]any, n)
	for i := 0; i < n; i++ {
		arr[i] = r.value()
	}
	return arr
}

func (r *mpReader) mapOf(n int) map[string]any {
	m := make(map[string]any, n)
	for i := 0; i < n; i++ {
		key := r.value()
		val := r.value()
		ks, _ := key.(string)
		m[ks] = val
	}
	return m
}
