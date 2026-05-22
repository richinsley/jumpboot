"use strict";
// jumpboot Node.js REPL — the analog of scripts/repl.py.
//
// It runs as the __main__ module of a NodeProgram, so the secondary bootstrap
// has already wired jumpboot.Pipe_in / Pipe_out / Status_in. It reads code
// blocks from Pipe_in (delimited by DELIMITER), evaluates each in a persistent
// vm context, captures console output, and writes the captured output +
// DELIMITER back on Pipe_out with an execution status on Status_in.
//
// State persistence: `var` declarations, function/class declarations and bare
// assignments persist across Execute calls (they become context globals).
// Top-level `let`/`const` are scoped to a single Execute call — a known
// JavaScript limitation; use `var` or assignment for state that must persist.

const fs = require("fs");
const vm = require("vm");
const util = require("util");
const jumpboot = require("jumpboot");

const DELIMITER = "\x01\x02\x03\n";

const inFd = jumpboot.Pipe_in; // host -> repl: code blocks
const outFd = jumpboot.Pipe_out; // repl -> host: captured output
const statusFd = jumpboot.Status_in; // repl -> host: execution status

// captureCombined mirrors the Python __CAPTURE_COMBINED__ flag: when true,
// console.error/warn are captured alongside console.log; when false, only
// console.log is captured and the rest go to the process's real stderr.
let captureCombined = true;
let captureBuf = "";

function fmtConsoleArg(x) {
	return typeof x === "string" ? x : util.inspect(x);
}

function appendCapture(args) {
	captureBuf += args.map(fmtConsoleArg).join(" ") + "\n";
}

const capturedConsole = {
	log: (...a) => appendCapture(a),
	info: (...a) => appendCapture(a),
	debug: (...a) => appendCapture(a),
	error: (...a) => {
		if (captureCombined) appendCapture(a);
		else process.stderr.write(a.map(fmtConsoleArg).join(" ") + "\n");
	},
	warn: (...a) => {
		if (captureCombined) appendCapture(a);
		else process.stderr.write(a.map(fmtConsoleArg).join(" ") + "\n");
	},
};

// The persistent evaluation context. V8 built-ins (Object, JSON, Math, Promise,
// ...) are present automatically; Node-injected globals are added explicitly.
const sandbox = {
	console: capturedConsole,
	require,
	process,
	Buffer,
	URL,
	URLSearchParams,
	TextEncoder,
	TextDecoder,
	setTimeout,
	clearTimeout,
	setInterval,
	clearInterval,
	setImmediate,
	clearImmediate,
	queueMicrotask,
};
sandbox.global = sandbox;
sandbox.globalThis = sandbox;
const context = vm.createContext(sandbox);

function writeAll(fd, text) {
	const data = Buffer.from(text, "utf8");
	let written = 0;
	while (written < data.length) {
		written += fs.writeSync(fd, data, written, data.length - written);
	}
}

function writeStatus(obj) {
	try {
		fs.writeSync(statusFd, JSON.stringify(obj) + "\n");
	} catch (e) {
		// best effort
	}
}

// handleBlock evaluates one code block and emits its output, status, and the
// terminating delimiter — the same sequence scripts/repl.py produces.
function handleBlock(block) {
	// A leading __CAPTURE_COMBINED__ assignment is a flag toggle, not code.
	// It produces no output, status, or delimiter (matching repl.py).
	if (block.startsWith("__CAPTURE_COMBINED__ =")) {
		captureCombined = block.split("=")[1].trim() === "True";
		return;
	}

	captureBuf = "";
	let status;
	try {
		const result = vm.runInContext(block, context, { filename: "<jumpboot-repl>" });
		if (result !== undefined) {
			captureBuf += util.inspect(result) + "\n";
		}
		status = { type: "status", message: "ok" };
	} catch (e) {
		captureBuf += (e && e.stack ? e.stack : String(e)) + "\n";
		status = {
			type: "exception",
			exception: e && e.name ? e.name : "Error",
			message: e && e.message ? e.message : String(e),
			traceback: e && e.stack ? e.stack : "",
		};
	}

	// Order matches repl.py: output first, then status, then delimiter.
	writeAll(outFd, captureBuf);
	writeStatus(status);
	writeAll(outFd, DELIMITER);
}

// Synchronous read loop: accumulate bytes from Pipe_in and dispatch each
// DELIMITER-terminated block. Returns (and the process exits) on EOF.
function runRepl() {
	let buf = Buffer.alloc(0);
	const chunk = Buffer.alloc(65536);
	const delim = Buffer.from(DELIMITER, "utf8");
	while (true) {
		let n;
		try {
			n = fs.readSync(inFd, chunk, 0, chunk.length, null);
		} catch (e) {
			if (e && e.code === "EAGAIN") continue;
			break;
		}
		if (n === 0) break; // EOF — the host closed the pipe
		buf = Buffer.concat([buf, chunk.subarray(0, n)]);
		let idx;
		while ((idx = buf.indexOf(delim)) !== -1) {
			const block = buf.subarray(0, idx).toString("utf8");
			buf = buf.subarray(idx + delim.length);
			handleBlock(block);
		}
	}
}

runRepl();
