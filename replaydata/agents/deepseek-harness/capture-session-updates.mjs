#!/usr/bin/env node
import { appendFileSync, writeFileSync } from "node:fs";

const [url, output, ready] = process.argv.slice(2);
if (!url || !output || !ready) process.exit(2);
const ws = new WebSocket(url);
ws.addEventListener("open", () => writeFileSync(ready, "open\n"));
ws.addEventListener("message", (event) => appendFileSync(output, `${event.data}\n`));
ws.addEventListener("error", () => process.exitCode = 1);
setInterval(() => {}, 1000);
