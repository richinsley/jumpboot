"use strict";
// jumpboot — Deno runtime SDK.
//
// This single self-contained file is concatenated ahead of a user plugin and
// run by `deno eval`. The plugin subclasses MessagePackQueueServer and
// constructs it; the server then speaks the jumpboot queue protocol
// (docs/PROTOCOL.md) over Deno.stdin / Deno.stdout, so an unmodified Go
// QueueProcess can drive it.
//
// It is dependency-free — it bundles its own MessagePack codec built on the
// Web platform APIs (Uint8Array, DataView, TextEncoder/TextDecoder) that Deno
// provides — so a plugin needs nothing installed.
//
// A plugin must not write to Deno.stdout: it is the protocol channel. Use
// Deno.stderr (or console.error) for diagnostics.

// ===== MessagePack codec ===================================================

const _textEncoder = new TextEncoder();
const _textDecoder = new TextDecoder();

function mpEncode(value) {
	const out = [];
	mpEncodeValue(value, out);
	return Uint8Array.from(out);
}

function mpEncodeValue(v, out) {
	if (v === null || v === undefined) {
		out.push(0xc0);
		return;
	}
	switch (typeof v) {
		case "boolean":
			out.push(v ? 0xc3 : 0xc2);
			return;
		case "number":
			mpEncodeNumber(v, out);
			return;
		case "string":
			mpEncodeString(v, out);
			return;
		case "object":
			if (v instanceof Uint8Array) {
				mpEncodeBinary(v, out);
			} else if (Array.isArray(v)) {
				mpEncodeArray(v, out);
			} else {
				mpEncodeMap(v, out);
			}
			return;
		default:
			throw new TypeError("msgpack: cannot encode " + typeof v);
	}
}

function pushBE(out, value, bytes) {
	const dv = new DataView(new ArrayBuffer(8));
	if (bytes <= 4) {
		dv.setUint32(0, value >>> 0);
		for (let i = 4 - bytes; i < 4; i++) out.push(dv.getUint8(i));
	} else {
		dv.setBigUint64(0, BigInt(value));
		for (let i = 0; i < 8; i++) out.push(dv.getUint8(i));
	}
}

function mpEncodeNumber(n, out) {
	if (Number.isInteger(n)) {
		if (n >= 0) {
			if (n < 0x80) out.push(n);
			else if (n < 0x100) out.push(0xcc, n);
			else if (n < 0x10000) {
				out.push(0xcd);
				pushBE(out, n, 2);
			} else if (n < 0x100000000) {
				out.push(0xce);
				pushBE(out, n, 4);
			} else {
				out.push(0xcf);
				pushBE(out, n, 8);
			}
		} else {
			if (n >= -0x20) out.push(n & 0xff);
			else if (n >= -0x80) out.push(0xd0, n & 0xff);
			else if (n >= -0x8000) {
				out.push(0xd1);
				const dv = new DataView(new ArrayBuffer(2));
				dv.setInt16(0, n);
				out.push(dv.getUint8(0), dv.getUint8(1));
			} else if (n >= -0x80000000) {
				out.push(0xd2);
				const dv = new DataView(new ArrayBuffer(4));
				dv.setInt32(0, n);
				for (let i = 0; i < 4; i++) out.push(dv.getUint8(i));
			} else {
				out.push(0xd3);
				const dv = new DataView(new ArrayBuffer(8));
				dv.setBigInt64(0, BigInt(n));
				for (let i = 0; i < 8; i++) out.push(dv.getUint8(i));
			}
		}
		return;
	}
	out.push(0xcb);
	const dv = new DataView(new ArrayBuffer(8));
	dv.setFloat64(0, n);
	for (let i = 0; i < 8; i++) out.push(dv.getUint8(i));
}

function mpEncodeString(s, out) {
	const data = _textEncoder.encode(s);
	const len = data.length;
	if (len < 0x20) out.push(0xa0 | len);
	else if (len < 0x100) out.push(0xd9, len);
	else if (len < 0x10000) {
		out.push(0xda);
		pushBE(out, len, 2);
	} else {
		out.push(0xdb);
		pushBE(out, len, 4);
	}
	for (let i = 0; i < len; i++) out.push(data[i]);
}

function mpEncodeBinary(data, out) {
	const len = data.length;
	if (len < 0x100) out.push(0xc4, len);
	else if (len < 0x10000) {
		out.push(0xc5);
		pushBE(out, len, 2);
	} else {
		out.push(0xc6);
		pushBE(out, len, 4);
	}
	for (let i = 0; i < len; i++) out.push(data[i]);
}

function mpEncodeArray(arr, out) {
	const len = arr.length;
	if (len < 0x10) out.push(0x90 | len);
	else if (len < 0x10000) {
		out.push(0xdc);
		pushBE(out, len, 2);
	} else {
		out.push(0xdd);
		pushBE(out, len, 4);
	}
	for (const item of arr) mpEncodeValue(item, out);
}

function mpEncodeMap(obj, out) {
	const keys = Object.keys(obj);
	const len = keys.length;
	if (len < 0x10) out.push(0x80 | len);
	else if (len < 0x10000) {
		out.push(0xde);
		pushBE(out, len, 2);
	} else {
		out.push(0xdf);
		pushBE(out, len, 4);
	}
	for (const key of keys) {
		mpEncodeString(key, out);
		mpEncodeValue(obj[key], out);
	}
}

function mpDecode(bytes) {
	const r = { dv: new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength), bytes, pos: 0 };
	return mpDecodeValue(r);
}

function mpDecodeValue(r) {
	const b = r.bytes[r.pos++];
	if (b <= 0x7f) return b;
	if (b >= 0xe0) return b - 0x100;
	if (b >= 0x80 && b <= 0x8f) return mpDecodeMap(r, b & 0x0f);
	if (b >= 0x90 && b <= 0x9f) return mpDecodeArray(r, b & 0x0f);
	if (b >= 0xa0 && b <= 0xbf) return mpDecodeStr(r, b & 0x1f);

	switch (b) {
		case 0xc0:
			return null;
		case 0xc2:
			return false;
		case 0xc3:
			return true;
		case 0xc4:
			return mpDecodeBin(r, r.bytes[r.pos++]);
		case 0xc5:
			return mpDecodeBin(r, readU(r, 2));
		case 0xc6:
			return mpDecodeBin(r, readU(r, 4));
		case 0xca: {
			const v = r.dv.getFloat32(r.pos);
			r.pos += 4;
			return v;
		}
		case 0xcb: {
			const v = r.dv.getFloat64(r.pos);
			r.pos += 8;
			return v;
		}
		case 0xcc:
			return r.bytes[r.pos++];
		case 0xcd:
			return readU(r, 2);
		case 0xce:
			return readU(r, 4);
		case 0xcf:
			return Number(r.dv.getBigUint64((r.pos += 8) - 8));
		case 0xd0:
			return r.dv.getInt8(r.pos++);
		case 0xd1: {
			const v = r.dv.getInt16(r.pos);
			r.pos += 2;
			return v;
		}
		case 0xd2: {
			const v = r.dv.getInt32(r.pos);
			r.pos += 4;
			return v;
		}
		case 0xd3:
			return Number(r.dv.getBigInt64((r.pos += 8) - 8));
		case 0xd9:
			return mpDecodeStr(r, r.bytes[r.pos++]);
		case 0xda:
			return mpDecodeStr(r, readU(r, 2));
		case 0xdb:
			return mpDecodeStr(r, readU(r, 4));
		case 0xdc:
			return mpDecodeArray(r, readU(r, 2));
		case 0xdd:
			return mpDecodeArray(r, readU(r, 4));
		case 0xde:
			return mpDecodeMap(r, readU(r, 2));
		case 0xdf:
			return mpDecodeMap(r, readU(r, 4));
		default:
			throw new Error("msgpack: unsupported type byte 0x" + b.toString(16));
	}
}

function readU(r, n) {
	let v;
	if (n === 2) v = r.dv.getUint16(r.pos);
	else v = r.dv.getUint32(r.pos);
	r.pos += n;
	return v;
}

function mpDecodeStr(r, len) {
	const s = _textDecoder.decode(r.bytes.subarray(r.pos, r.pos + len));
	r.pos += len;
	return s;
}

function mpDecodeBin(r, len) {
	const b = r.bytes.slice(r.pos, r.pos + len);
	r.pos += len;
	return b;
}

function mpDecodeArray(r, len) {
	const arr = new Array(len);
	for (let i = 0; i < len; i++) arr[i] = mpDecodeValue(r);
	return arr;
}

function mpDecodeMap(r, len) {
	const obj = {};
	for (let i = 0; i < len; i++) {
		const key = mpDecodeValue(r);
		obj[key] = mpDecodeValue(r);
	}
	return obj;
}

// ===== framed transport over Deno stdio ====================================

function writeFrame(payload) {
	const frame = new Uint8Array(4 + payload.length);
	new DataView(frame.buffer).setUint32(0, payload.length);
	frame.set(payload, 4);
	let written = 0;
	while (written < frame.length) {
		written += Deno.stdout.writeSync(frame.subarray(written));
	}
}

function concatBytes(a, b) {
	if (a.length === 0) return b;
	const out = new Uint8Array(a.length + b.length);
	out.set(a, 0);
	out.set(b, a.length);
	return out;
}

// ===== queue server ========================================================

const BUILTINS = new Set(["__get_methods__", "__cancel__", "exit", "shutdown"]);

class MessagePackQueueServer {
	constructor() {
		this.running = true;
		this._handlers = new Map();
		this._inflight = new Map();
		this._exposeMethods();
		this._readLoop();
	}

	_exposeMethods() {
		let proto = Object.getPrototypeOf(this);
		const stop = MessagePackQueueServer.prototype;
		while (proto && proto !== stop && proto !== Object.prototype) {
			for (const name of Object.getOwnPropertyNames(proto)) {
				if (name === "constructor" || name.startsWith("_")) continue;
				if (BUILTINS.has(name) || this._handlers.has(name)) continue;
				const fn = proto[name];
				if (typeof fn !== "function") continue;
				this._handlers.set(name, fn.bind(this));
			}
			proto = Object.getPrototypeOf(proto);
		}
	}

	registerHandler(name, fn) {
		this._handlers.set(name, fn);
	}

	async _readLoop() {
		const buf = new Uint8Array(65536);
		let acc = new Uint8Array(0);
		while (true) {
			const n = await Deno.stdin.read(buf);
			if (n === null) {
				// EOF — the host closed the connection.
				Deno.exit(0);
			}
			acc = concatBytes(acc, buf.subarray(0, n));
			while (acc.length >= 4) {
				const len = new DataView(acc.buffer, acc.byteOffset, 4).getUint32(0);
				if (acc.length < 4 + len) break;
				const payload = acc.slice(4, 4 + len);
				acc = acc.slice(4 + len);
				let msg;
				try {
					msg = mpDecode(payload);
				} catch (e) {
					continue;
				}
				this._onMessage(msg);
			}
		}
	}

	_onMessage(msg) {
		if (!msg || typeof msg !== "object") return;
		// The Deno SDK only receives commands from the host; it does not
		// initiate requests, so every frame here is a command to dispatch.
		this._dispatch(msg.command, msg.data, msg.request_id, !!msg.stream);
	}

	async _dispatch(command, data, requestId, stream) {
		if (command === "exit") {
			if (requestId != null) this._sendResponse({ status: "exiting" }, requestId);
			Deno.exit(0);
		}
		if (command === "shutdown") {
			if (requestId != null) this._sendResponse({ status: "shutting_down" }, requestId);
			Deno.exit(0);
		}
		if (command === "__get_methods__") {
			this._sendResponse({ methods: this._describeMethods() }, requestId);
			return;
		}
		if (command === "__cancel__") {
			this._sendResponse(this._handleCancel(data), requestId);
			return;
		}

		const handler = this._handlers.get(command);
		if (!handler) {
			this._sendResponse({ error: "Unknown command: " + command }, requestId);
			return;
		}

		const inflight = { cancelled: false };
		if (requestId) this._inflight.set(requestId, inflight);
		const ctx = {
			requestId,
			cancelled: () => inflight.cancelled,
			emit: (chunk) => {
				if (!stream || !requestId) return;
				if (chunk && typeof chunk === "object" && !Array.isArray(chunk) && !(chunk instanceof Uint8Array)) {
					this._sendResponse(Object.assign({}, chunk, { done: false }), requestId);
				} else {
					this._sendResponse({ result: chunk, done: false }, requestId);
				}
			},
		};

		try {
			const result = await handler(data, requestId, ctx);
			if (requestId != null) {
				if (stream) this._sendResponse({ result, done: true }, requestId);
				else this._sendResponse(result, requestId);
			}
		} catch (e) {
			if (requestId != null) {
				const errResp = { error: e && e.message ? e.message : String(e) };
				if (e && e.stack) errResp.traceback = e.stack;
				if (stream) errResp.done = true;
				this._sendResponse(errResp, requestId);
			}
		} finally {
			if (requestId) this._inflight.delete(requestId);
		}
	}

	_handleCancel(data) {
		const target = data && data.target_request_id;
		if (!target) return { ok: false, error: "missing target_request_id" };
		const inflight = this._inflight.get(target);
		if (!inflight) return { ok: false, error: "no in-flight call", target_request_id: target };
		inflight.cancelled = true;
		return { ok: true, target_request_id: target };
	}

	_describeMethods() {
		const methods = {};
		for (const name of this._handlers.keys()) {
			methods[name] = { parameters: [], return: {}, doc: "" };
		}
		return methods;
	}

	_sendResponse(response, requestId) {
		let payload;
		if (response && typeof response === "object" && !Array.isArray(response) && !(response instanceof Uint8Array)) {
			payload = response;
			payload.request_id = requestId;
		} else {
			payload = { result: response, request_id: requestId };
		}
		try {
			writeFrame(mpEncode(payload));
		} catch (e) {
			// best-effort; the host pipe may already be gone
		}
	}
}

// expose is an optional marker for API parity with Python's @exposed
// decorator; public methods are auto-exposed regardless.
function expose(fn) {
	if (fn) fn._exposed = true;
	return fn;
}
