"use strict";
// Minimal, dependency-free MessagePack codec for the jumpboot queue protocol.
//
// It implements the subset of the MessagePack spec the protocol uses — nil,
// bool, int, float64, str, bin, array, map — and interoperates with the Go
// host's github.com/vmihailenco/msgpack/v5. It is bundled (not npm-installed)
// so a Node.js QueueProcess works the moment the environment is created, the
// same way the Python package bundles a pure-Python msgpack fallback.
//
// See docs/PROTOCOL.md for the framing that wraps these payloads.

// ---- encode ---------------------------------------------------------------

function encode(value) {
	const chunks = [];
	encodeValue(value, chunks);
	return Buffer.concat(chunks);
}

function encodeValue(v, out) {
	if (v === null || v === undefined) {
		out.push(Buffer.from([0xc0]));
		return;
	}
	switch (typeof v) {
		case "boolean":
			out.push(Buffer.from([v ? 0xc3 : 0xc2]));
			return;
		case "number":
			encodeNumber(v, out);
			return;
		case "string":
			encodeString(v, out);
			return;
		case "object":
			if (Buffer.isBuffer(v) || v instanceof Uint8Array) {
				encodeBinary(Buffer.from(v), out);
			} else if (Array.isArray(v)) {
				encodeArray(v, out);
			} else {
				encodeMap(v, out);
			}
			return;
		default:
			throw new TypeError("msgpack: cannot encode value of type " + typeof v);
	}
}

function encodeNumber(n, out) {
	if (Number.isInteger(n)) {
		if (n >= 0) {
			if (n < 0x80) {
				out.push(Buffer.from([n]));
			} else if (n < 0x100) {
				out.push(Buffer.from([0xcc, n]));
			} else if (n < 0x10000) {
				const b = Buffer.alloc(3);
				b[0] = 0xcd;
				b.writeUInt16BE(n, 1);
				out.push(b);
			} else if (n < 0x100000000) {
				const b = Buffer.alloc(5);
				b[0] = 0xce;
				b.writeUInt32BE(n, 1);
				out.push(b);
			} else {
				const b = Buffer.alloc(9);
				b[0] = 0xcf;
				b.writeBigUInt64BE(BigInt(n), 1);
				out.push(b);
			}
		} else {
			if (n >= -0x20) {
				out.push(Buffer.from([n & 0xff]));
			} else if (n >= -0x80) {
				out.push(Buffer.from([0xd0, n & 0xff]));
			} else if (n >= -0x8000) {
				const b = Buffer.alloc(3);
				b[0] = 0xd1;
				b.writeInt16BE(n, 1);
				out.push(b);
			} else if (n >= -0x80000000) {
				const b = Buffer.alloc(5);
				b[0] = 0xd2;
				b.writeInt32BE(n, 1);
				out.push(b);
			} else {
				const b = Buffer.alloc(9);
				b[0] = 0xd3;
				b.writeBigInt64BE(BigInt(n), 1);
				out.push(b);
			}
		}
		return;
	}
	// Non-integer: encode as float64.
	const b = Buffer.alloc(9);
	b[0] = 0xcb;
	b.writeDoubleBE(n, 1);
	out.push(b);
}

function encodeString(s, out) {
	const data = Buffer.from(s, "utf8");
	const len = data.length;
	if (len < 0x20) {
		out.push(Buffer.from([0xa0 | len]));
	} else if (len < 0x100) {
		out.push(Buffer.from([0xd9, len]));
	} else if (len < 0x10000) {
		const h = Buffer.alloc(3);
		h[0] = 0xda;
		h.writeUInt16BE(len, 1);
		out.push(h);
	} else {
		const h = Buffer.alloc(5);
		h[0] = 0xdb;
		h.writeUInt32BE(len, 1);
		out.push(h);
	}
	out.push(data);
}

function encodeBinary(data, out) {
	const len = data.length;
	if (len < 0x100) {
		out.push(Buffer.from([0xc4, len]));
	} else if (len < 0x10000) {
		const h = Buffer.alloc(3);
		h[0] = 0xc5;
		h.writeUInt16BE(len, 1);
		out.push(h);
	} else {
		const h = Buffer.alloc(5);
		h[0] = 0xc6;
		h.writeUInt32BE(len, 1);
		out.push(h);
	}
	out.push(data);
}

function encodeArray(arr, out) {
	const len = arr.length;
	if (len < 0x10) {
		out.push(Buffer.from([0x90 | len]));
	} else if (len < 0x10000) {
		const h = Buffer.alloc(3);
		h[0] = 0xdc;
		h.writeUInt16BE(len, 1);
		out.push(h);
	} else {
		const h = Buffer.alloc(5);
		h[0] = 0xdd;
		h.writeUInt32BE(len, 1);
		out.push(h);
	}
	for (const item of arr) encodeValue(item, out);
}

function encodeMap(obj, out) {
	const keys = Object.keys(obj);
	const len = keys.length;
	if (len < 0x10) {
		out.push(Buffer.from([0x80 | len]));
	} else if (len < 0x10000) {
		const h = Buffer.alloc(3);
		h[0] = 0xde;
		h.writeUInt16BE(len, 1);
		out.push(h);
	} else {
		const h = Buffer.alloc(5);
		h[0] = 0xdf;
		h.writeUInt32BE(len, 1);
		out.push(h);
	}
	for (const key of keys) {
		encodeString(key, out);
		encodeValue(obj[key], out);
	}
}

// ---- decode ---------------------------------------------------------------

// decode reads exactly one MessagePack value from buf and returns it. The
// caller frames messages by length (see transport.js), so trailing bytes are
// not expected.
function decode(buf) {
	const reader = { buf, pos: 0 };
	return decodeValue(reader);
}

function decodeValue(r) {
	const b = r.buf[r.pos++];

	if (b <= 0x7f) return b; // positive fixint
	if (b >= 0xe0) return b - 0x100; // negative fixint
	if (b >= 0x80 && b <= 0x8f) return decodeMap(r, b & 0x0f); // fixmap
	if (b >= 0x90 && b <= 0x9f) return decodeArray(r, b & 0x0f); // fixarray
	if (b >= 0xa0 && b <= 0xbf) return decodeStr(r, b & 0x1f); // fixstr

	switch (b) {
		case 0xc0:
			return null;
		case 0xc2:
			return false;
		case 0xc3:
			return true;
		case 0xc4:
			return decodeBin(r, readUint(r, 1));
		case 0xc5:
			return decodeBin(r, readUint(r, 2));
		case 0xc6:
			return decodeBin(r, readUint(r, 4));
		case 0xca: {
			const v = r.buf.readFloatBE(r.pos);
			r.pos += 4;
			return v;
		}
		case 0xcb: {
			const v = r.buf.readDoubleBE(r.pos);
			r.pos += 8;
			return v;
		}
		case 0xcc:
			return readUint(r, 1);
		case 0xcd:
			return readUint(r, 2);
		case 0xce:
			return readUint(r, 4);
		case 0xcf:
			return readUint(r, 8);
		case 0xd0:
			return readInt(r, 1);
		case 0xd1:
			return readInt(r, 2);
		case 0xd2:
			return readInt(r, 4);
		case 0xd3:
			return readInt(r, 8);
		case 0xd9:
			return decodeStr(r, readUint(r, 1));
		case 0xda:
			return decodeStr(r, readUint(r, 2));
		case 0xdb:
			return decodeStr(r, readUint(r, 4));
		case 0xdc:
			return decodeArray(r, readUint(r, 2));
		case 0xdd:
			return decodeArray(r, readUint(r, 4));
		case 0xde:
			return decodeMap(r, readUint(r, 2));
		case 0xdf:
			return decodeMap(r, readUint(r, 4));
		default:
			throw new Error("msgpack: unsupported type byte 0x" + b.toString(16));
	}
}

function readUint(r, n) {
	let v;
	if (n === 1) {
		v = r.buf[r.pos];
	} else if (n === 2) {
		v = r.buf.readUInt16BE(r.pos);
	} else if (n === 4) {
		v = r.buf.readUInt32BE(r.pos);
	} else {
		// 8 bytes — collapse to a JS number (safe for our protocol's values).
		v = Number(r.buf.readBigUInt64BE(r.pos));
	}
	r.pos += n;
	return v;
}

function readInt(r, n) {
	let v;
	if (n === 1) {
		v = r.buf.readInt8(r.pos);
	} else if (n === 2) {
		v = r.buf.readInt16BE(r.pos);
	} else if (n === 4) {
		v = r.buf.readInt32BE(r.pos);
	} else {
		v = Number(r.buf.readBigInt64BE(r.pos));
	}
	r.pos += n;
	return v;
}

function decodeStr(r, len) {
	const s = r.buf.toString("utf8", r.pos, r.pos + len);
	r.pos += len;
	return s;
}

function decodeBin(r, len) {
	const b = r.buf.subarray(r.pos, r.pos + len);
	r.pos += len;
	return Buffer.from(b);
}

function decodeArray(r, len) {
	const arr = new Array(len);
	for (let i = 0; i < len; i++) arr[i] = decodeValue(r);
	return arr;
}

function decodeMap(r, len) {
	const obj = {};
	for (let i = 0; i < len; i++) {
		const key = decodeValue(r);
		obj[key] = decodeValue(r);
	}
	return obj;
}

module.exports = { encode, decode };
