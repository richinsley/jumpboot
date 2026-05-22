"use strict";
// jumpboot Node.js primary bootstrap — the analog of scripts/bootstrap.py.
//
// It is passed to `node -e` and does the minimum possible: read the (larger)
// secondary bootstrap from the bootstrap pipe fd handed to it as argv[2], then
// eval it. Keeping this stage tiny avoids any command-line length limits on
// the secondary bootstrap and the program data.
const fs = require("fs");
const secondary = fs.readFileSync(parseInt(process.argv[2], 10), "utf8");
eval(secondary);
