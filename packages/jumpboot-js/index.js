"use strict";
// jumpboot — Node.js runtime SDK. A user program embedded by a Go NodeProcess
// does `const { MessagePackQueueServer } = require('jumpboot')` to expose
// methods the Go host can Call(). This is the counterpart of the embedded
// Python package in packages/jumpboot/.

const config = require("./config");
const { MessagePackQueueServer } = require("./queue-server");

// expose is an optional marker for API parity with Python's @exposed
// decorator. Public methods are auto-exposed regardless; expose() simply
// returns the function unchanged.
function expose(fn) {
	if (fn) fn._exposed = true;
	return fn;
}

// setRuntime is called by the secondary bootstrap to hand the SDK its pipe
// file descriptors and key/value pairs before the user program runs. The fds
// and KVPairs are then reachable as jumpboot.Pipe_in / jumpboot.<key>.
function setRuntime(pipeInFd, pipeOutFd, statusFd, kvpairs) {
	config.pipeInFd = pipeInFd;
	config.pipeOutFd = pipeOutFd;
	config.statusFd = statusFd;
	config.kvpairs = kvpairs || {};
	module.exports.Pipe_in = pipeInFd;
	module.exports.Pipe_out = pipeOutFd;
	module.exports.Status_in = statusFd;
	for (const key of Object.keys(config.kvpairs)) {
		module.exports[key] = config.kvpairs[key];
	}
}

module.exports = { MessagePackQueueServer, expose, setRuntime, config };
