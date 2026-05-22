"use strict";
// MessagePackQueueServer — the Node.js queue server. It is the counterpart of
// the Python MessagePackQueueServer in packages/jumpboot/msgpackqueue.py and
// speaks the identical wire protocol (docs/PROTOCOL.md), so an unmodified Go
// QueueProcess can drive it.
//
// Subclass it and define public async methods; they are auto-exposed as
// commands the Go host can Call(). Methods receive (data, requestId, ctx)
// where data is the decoded argument payload and ctx offers cancelled() and
// emit() for cooperative cancellation and streaming.

const config = require("./config");
const { MessagePackTransport } = require("./transport");

const BUILTINS = new Set(["__get_methods__", "__cancel__", "exit", "shutdown"]);

class MessagePackQueueServer {
	constructor(opts = {}) {
		this.running = true;
		this._handlers = new Map(); // command name -> bound async fn
		this._pending = new Map(); // py-N -> {resolve}
		this._inflight = new Map(); // request_id -> {cancelled:bool}
		this._nextId = 0;

		const readFd = opts.readFd != null ? opts.readFd : config.pipeInFd;
		const writeFd = opts.writeFd != null ? opts.writeFd : config.pipeOutFd;
		this._transport = new MessagePackTransport(readFd, writeFd);

		if (opts.exposeMethods !== false) this._exposeMethods();

		this._transport.start(
			(msg) => this._onMessage(msg),
			() => this._handleClose()
		);
	}

	// _exposeMethods registers every public method declared on subclasses
	// (the prototype chain between the instance and MessagePackQueueServer).
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

	// registerHandler exposes an additional command handler explicitly.
	registerHandler(name, fn) {
		this._handlers.set(name, fn);
	}

	_onMessage(msg) {
		if (!msg || typeof msg !== "object") return;
		const requestId = msg.request_id;
		// A frame carrying an id we generated (py-) is a response to one of
		// our own requests; anything else is a command from the Go host.
		if (typeof requestId === "string" && requestId.startsWith("py-")) {
			const pend = this._pending.get(requestId);
			if (pend) {
				this._pending.delete(requestId);
				pend.resolve(msg);
			}
			return;
		}
		this._dispatch(msg.command, msg.data, requestId, !!msg.stream);
	}

	async _dispatch(command, data, requestId, stream) {
		if (command === "exit") {
			this._handleExitCommand(requestId);
			return;
		}
		if (command === "shutdown") {
			this._handleShutdown(requestId);
			return;
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
			// emit publishes a partial frame on a streaming call. A plain
			// object is sent flat with done:false (matching the Python side);
			// any other value is wrapped as {result, done:false}.
			emit: (chunk) => {
				if (!stream || !requestId) return;
				if (
					chunk &&
					typeof chunk === "object" &&
					!Array.isArray(chunk) &&
					!Buffer.isBuffer(chunk)
				) {
					this._sendResponse(
						Object.assign({}, chunk, { done: false }),
						requestId
					);
				} else {
					this._sendResponse({ result: chunk, done: false }, requestId);
				}
			},
		};

		try {
			const result = await handler(data, requestId, ctx);
			if (requestId != null) {
				if (stream) {
					this._sendResponse({ result, done: true }, requestId);
				} else {
					this._sendResponse(result, requestId);
				}
			}
		} catch (e) {
			if (requestId != null) {
				const errResp = {
					error: e && e.message ? e.message : String(e),
				};
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
		if (!inflight) {
			return { ok: false, error: "no in-flight call", target_request_id: target };
		}
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

	// _sendResponse mirrors the Python send_response: a plain object is sent
	// as-is with request_id added; any other value is wrapped as {result,...}.
	_sendResponse(response, requestId) {
		let payload;
		if (
			response &&
			typeof response === "object" &&
			!Array.isArray(response) &&
			!Buffer.isBuffer(response)
		) {
			payload = response;
			payload.request_id = requestId;
		} else {
			payload = { result: response, request_id: requestId };
		}
		try {
			this._transport.send(payload);
		} catch (e) {
			// best-effort; the host pipe may already be gone
		}
	}

	_handleExitCommand(requestId) {
		if (requestId != null) {
			try {
				this._sendResponse({ status: "exiting" }, requestId);
			} catch (e) {
				// ignore
			}
		}
		process.exit(0);
	}

	_handleShutdown(requestId) {
		if (requestId != null) {
			this._sendResponse({ status: "shutting_down" }, requestId);
		}
		this.running = false;
		this._transport.close();
		process.exit(0);
	}

	_handleClose() {
		// The Go host closed the pipe — there is nothing left to serve.
		this.running = false;
		process.exit(0);
	}
}

module.exports = { MessagePackQueueServer };
