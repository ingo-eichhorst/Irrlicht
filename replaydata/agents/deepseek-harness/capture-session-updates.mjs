#!/usr/bin/env node
import { appendFileSync, writeFileSync } from "node:fs";

const [url, output, ready] = process.argv.slice(2);
if (!url || !output || !ready) process.exit(2);
let stopping = false;
const ws = new WebSocket(url);
const keepAlive = setInterval(() => {}, 1000);

function finish(code) {
  clearInterval(keepAlive);
  process.exit(code);
}

ws.addEventListener("open", () => writeFileSync(ready, "open\n"));
ws.addEventListener("message", (event) => appendFileSync(output, `${event.data}\n`));
ws.addEventListener("error", () => finish(1));
ws.addEventListener("close", () => finish(stopping ? 0 : 1));

process.on("SIGTERM", () => {
  stopping = true;
  ws.close();
  setTimeout(() => finish(0), 1000).unref();
});
