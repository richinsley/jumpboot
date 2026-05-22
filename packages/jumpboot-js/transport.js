"use strict";
// MessagePackTransport — length-prefixed MessagePack framing over pipe file
// descriptors. This is the Node.js counterpart of MsgpackTransport in
// ipcmessagepack.go and MessagePackTransport in msgpackqueue.py. See
// docs/PROTOCOL.md §1 for the wire framing.

const fs = require("fs");
const msgpack = require("./msgpack");

class MessagePackTransport {
	constructor(readFd, writeFd) {
		this.readFd = readFd;
		this.writeFd = writeFd;
		this._buf = Buffer.alloc(0);
		this._stream = null;
		this._onMessage = null;
		this._onClose = null;
	}

	// start begins reading frames. onMessage is invoked with each decoded
	// message object; onClose is invoked once the read end reaches EOF.
	start(onMessage, onClose) {
		this._onMessage = onMessage;
		this._onClose = onClose || (() => {});
		this._stream = fs.createReadStream(null, {
			fd: this.readFd,
			autoClose: false,
		});
		this._stream.on("data", (chunk) => this._feed(chunk));
		this._stream.on("end", () => this._onClose());
		this._stream.on("error", () => this._onClose());
	}

	_feed(chunk) {
		this._buf = this._buf.length ? Buffer.concat([this._buf, chunk]) : chunk;
		// A pipe read may deliver several frames at once or split one across
		// chunks; drain every complete frame the buffer currently holds.
		while (this._buf.length >= 4) {
			const len = this._buf.readUInt32BE(0);
			if (this._buf.length < 4 + len) break;
			const payload = Buffer.from(this._buf.subarray(4, 4 + len));
			this._buf = this._buf.subarray(4 + len);
			let msg;
			try {
				msg = msgpack.decode(payload);
			} catch (e) {
				// Drop a corrupt frame rather than wedging the loop.
				continue;
			}
			this._onMessage(msg);
		}
	}

	// send encodes obj and writes a single length-prefixed frame. Writes loop
	// to tolerate a short write on a busy pipe.
	send(obj) {
		const payload = msgpack.encode(obj);
		const frame = Buffer.allocUnsafe(4 + payload.length);
		frame.writeUInt32BE(payload.length, 0);
		payload.copy(frame, 4);
		let written = 0;
		while (written < frame.length) {
			written += fs.writeSync(
				this.writeFd,
				frame,
				written,
				frame.length - written
			);
		}
	}

	close() {
		if (this._stream) {
			try {
				this._stream.destroy();
			} catch (e) {
				// ignore
			}
		}
	}
}

module.exports = { MessagePackTransport };
