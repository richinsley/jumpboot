"use strict";
// Mutable runtime configuration shared between the secondary bootstrap and the
// queue server. It is its own dependency-free module so both index.js and
// queue-server.js can require it without forming a circular dependency.
module.exports = {
	pipeInFd: -1, // fd the queue server READS commands from (Go -> Node)
	pipeOutFd: -1, // fd the queue server WRITES responses to (Node -> Go)
	statusFd: -1, // fd for status / exception reporting
	kvpairs: {},
};
