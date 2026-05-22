"use strict";
// jumpboot Node.js secondary bootstrap — the analog of
// scripts/secondaryBootstrapScript.py. It is eval'd by scripts/bootstrap.js.
//
// Responsibilities:
//   1. Read the NodeProgram JSON from the program pipe (argv[3]).
//   2. Materialise embedded packages/modules into an in-memory module
//      registry and install a require() hook so they load without ever
//      touching disk — the analog of Python's CustomFinder / CustomLoader.
//   3. Hand the queue server its pipe file descriptors via setRuntime().
//   4. Run the main module, reporting exceptions and exit on the status pipe.
(function jumpbootSecondaryBootstrap() {
	const fs = require("fs");
	const path = require("path");
	const Module = require("module");

	// --- read program data from the program pipe ---
	const programFd = parseInt(process.argv[3], 10);
	const program = JSON.parse(fs.readFileSync(programFd, "utf8"));

	const statusFd = program.StatusIn;
	function writeStatus(obj) {
		try {
			fs.writeSync(statusFd, JSON.stringify(obj) + "\n");
		} catch (e) {
			// best effort — the status pipe may already be closed
		}
	}

	// --- build the in-memory module registry ---
	// registry:   virtual filename -> source string
	// nameToFile: bare specifier   -> virtual filename of its entry point
	const registry = new Map();
	const nameToFile = new Map();
	const VROOT = "/jumpboot-virtual";

	function decodeSource(b64) {
		return Buffer.from(b64 || "", "base64").toString("utf8");
	}

	function addPackage(pkg, parentDir) {
		const dir = path.posix.join(parentDir, pkg.Name);
		let entry = null;
		for (const mod of pkg.Modules || []) {
			const filename = path.posix.join(dir, mod.Name);
			registry.set(filename, decodeSource(mod.Source));
			if (mod.Name === "index.js") entry = filename;
		}
		// require('<pkg.Name>') resolves to the package's index.js.
		if (entry) nameToFile.set(pkg.Name, entry);
		for (const sub of pkg.Packages || []) addPackage(sub, dir);
	}

	for (const pkg of program.Packages || []) addPackage(pkg, VROOT);

	for (const mod of program.Modules || []) {
		const filename = path.posix.join(VROOT, mod.Name);
		registry.set(filename, decodeSource(mod.Source));
		nameToFile.set(mod.Name.replace(/\.js$/, ""), filename);
	}

	// --- install the require() hook (mirrors Python's CustomFinder) ---
	function compileVirtual(filename) {
		const cached = Module._cache[filename];
		if (cached) return cached;
		const m = new Module(filename, null);
		m.filename = filename;
		m.paths = [];
		Module._cache[filename] = m;
		try {
			m._compile(registry.get(filename), filename);
		} catch (e) {
			delete Module._cache[filename];
			throw e;
		}
		m.loaded = true;
		return m;
	}

	const origLoad = Module._load;
	Module._load = function (request, parent, isMain) {
		// Bare specifier registered as a package/module entry point.
		if (nameToFile.has(request)) {
			return compileVirtual(nameToFile.get(request)).exports;
		}
		// Relative require from inside an already-virtual module.
		if (
			(request.startsWith("./") || request.startsWith("../")) &&
			parent &&
			parent.filename &&
			registry.has(parent.filename)
		) {
			const base = path.posix.dirname(parent.filename);
			const resolved = path.posix.resolve(base, request);
			const candidates = [
				resolved,
				resolved + ".js",
				path.posix.join(resolved, "index.js"),
			];
			for (const cand of candidates) {
				if (registry.has(cand)) return compileVirtual(cand).exports;
			}
		}
		return origLoad.apply(this, arguments);
	};

	// --- hand the queue server its pipe file descriptors ---
	const jumpboot = require("jumpboot");
	jumpboot.setRuntime(
		program.PipeIn,
		program.PipeOut,
		program.StatusIn,
		program.KVPairs || {}
	);

	// --- parent watchdog: exit if the parent process goes away ---
	const originalPpid = process.ppid;
	const watchdog = setInterval(() => {
		if (process.ppid !== originalPpid) {
			process.exit(1);
		}
	}, 3000);
	watchdog.unref();

	// --- report exit and async failures on the status pipe ---
	process.on("exit", () => writeStatus({ type: "status", message: "exit" }));
	function reportException(err) {
		writeStatus({
			type: "exception",
			exception: err && err.name ? err.name : "Error",
			message: err && err.message ? err.message : String(err),
			traceback: err && err.stack ? err.stack : "",
		});
		process.exit(1);
	}
	process.on("uncaughtException", reportException);
	process.on("unhandledRejection", (reason) => {
		reportException(reason instanceof Error ? reason : new Error(String(reason)));
	});

	// --- run the main module ---
	const mainName = program.Program.Name || "__main__";
	const mainFile = path.posix.join(
		VROOT,
		"__main__",
		mainName.endsWith(".js") ? mainName : mainName + ".js"
	);
	registry.set(mainFile, decodeSource(program.Program.Source));
	try {
		compileVirtual(mainFile);
	} catch (err) {
		reportException(err);
	}
})();
