// Irrlicht: each Herdr agent's context-window fill, read from the local
// Irrlicht daemon (GET /api/v1/sessions). Read-only: it writes no file and
// sends the daemon nothing but that GET. The only programs it runs are
// /usr/bin/printenv (twice, at start, for IRRLICHT_BIND_ADDR and
// IRRLICHT_HOME: a mod has no process.env) and /bin/sh's `[ -f ] && [ ! -L ]`
// test of the addr file (a mod has no lstat), at start and at most every 30 s
// while the daemon does not answer (lib/poll.js RERESOLVE_MS). The decisions
// live in ./lib, which imports nothing from Herdr; this file only reads Herdr,
// starts the timer and draws.

import { View, div, image, svg } from "gpui-kit";
import { v_flex, h_flex } from "gpui-base";
import { agents, terminals, theme, run, exec, home, readTextFile, checking, nextCommand } from "herdr";
import { createPoller } from "./lib/poll.js";
import { focusAddress, panelRows, signature, statusLine } from "./lib/rows.js";

const POLL_MS = 3000;

// envValue is one variable of Herdr's own environment, which `exec` hands its
// programs: printenv exits 1 for an unset variable and prints the value plus
// one newline otherwise.
async function envValue(name) {
  const r = await exec("/usr/bin/printenv", [name], { timeout: 5000 });
  return r.code === 0 ? r.stdout.replace(/\n$/, "") : undefined;
}

// isRegularFile is publishedAddr's os.Lstat + Mode().IsRegular(): true for a
// regular file that is not a symlink. `readTextFile` follows symlinks
// (measured in a live mod: a link to the addr file read the file's text).
async function isRegularFile(path) {
  const r = await exec("/bin/sh", ["-c", '[ -f "$1" ] && [ ! -L "$1" ]', "irrlicht", path], { timeout: 5000 });
  return r.code === 0;
}

function programIcon(p, color) {
  if (!p?.icon) return null;
  return p.tint ? svg(p.icon).size(16).flex_none().text_color(color) : image(p.icon).w(16).h(16).flex_none();
}

function focusAgent(address) {
  run("pane.focus", { terminal: address });
}

export default class Irrlicht extends View {
  init(_props, cx) {
    this.state = { phase: "starting" };
    this.sig = signature(this.state, []);
    if (checking()) return;
    const env = Promise.all([envValue("IRRLICHT_BIND_ADDR"), envValue("IRRLICHT_HOME")]);
    this.poller = createPoller({
      ladder: {
        bindAddr: async () => (await env)[0],
        irrlichtHome: async () => (await env)[1],
        home: async () => home(),
        isRegularFile,
        read: (path) => readTextFile(path),
      },
      fetch: (url) => fetch(url),
      now: () => Date.now(),
    });
    cx.timer.every(POLL_MS, (cx) => this.tick(cx));
    this.tick(cx);
    // Keyboard and script access to the row action: "irrlicht.focus-<n>"
    // focuses the agent in row n (from 1), exactly as clicking that row does.
    cx.spawn(async () => {
      while (!this.closed) {
        const address = focusAddress(await nextCommand(), agents()); // NOSONAR (javascript:S9382): commands arrive one at a time; waiting for the next is the loop
        if (address) focusAgent(address);
      }
    });
  }

  // close is Herdr's last call to a copy; it ends the command loop above.
  async close() {
    this.closed = true;
  }

  tick(cx) {
    const step = this.poller.step();
    if (step.kind === "stalled") this.show(cx, step.state);
    if (step.kind === "request") cx.spawn(async (cx) => this.show(cx, await step.request));
  }

  // show adopts a state and redraws only when what is drawn changed. Without
  // sessions no row is matched, so the terminals need not be read.
  show(cx, next) {
    this.state = next;
    const sessions = next.sessions ?? null;
    const sig = signature(next, panelRows(agents(), sessions ? terminals() : [], sessions));
    if (sig !== this.sig) {
      this.sig = sig;
      cx.notify();
    }
  }

  gauge(t, g) {
    if (g.kind === "percent") {
      const fill = t[g.color] ?? t.foreground;
      return h_flex().flex_none().gap(6).items_center()
        .child(div().w(36).h(4).rounded(2).bg(t.border)
          .child(div().h(4).rounded(2).w(Math.max(2, Math.round(36 * g.fraction))).bg(fill)))
        .child(div().w(40).text_right().text_size(11.5).text_color(t[g.textColor] ?? fill).child(g.text));
    }
    if (g.kind === "tokens") {
      return div().flex_none().text_size(11.5).text_color(t.muted).child(g.text + " tok");
    }
    return div().flex_none().w(40).text_right().text_size(11.5).text_color(t.dim).child("–");
  }

  row(t, r, i) {
    const pic = programIcon(r.program, t.foreground);
    return h_flex()
      .id(`ir-${r.address}`)
      .px(8)
      .h(30)
      .flex_none()
      .rounded(8)
      .items_center()
      .gap(8)
      .when(r.focused, (el) => el.bg(t.selected))
      .cursor_pointer()
      .hover((s) => s.bg(t.hover))
      .tooltip(`${i + 1}. ${r.tooltip}`)
      .on_click(() => focusAgent(r.address))
      .when(Boolean(pic), (el) => el.child(pic))
      .child(div().flex_1().min_w(0).truncate().text_color(t.foreground).child(r.label))
      .child(this.gauge(t, r.gauge))
      .child(div().size(7).flex_none().rounded(4).bg(t[r.status] ?? t.dim));
  }

  render() {
    const t = theme();
    const rows = panelRows(agents(), terminals(), this.state.sessions ?? null);
    const line = statusLine(this.state);
    return v_flex()
      .size_full()
      .min_h(0)
      .bg(t.chrome)
      .text_size(13)
      .child(h_flex().px(16).h(41).pt(14).pb(5).flex_none().items_center()
        .child(div().text_size(11.5).font_medium().text_color(t.dim).child("Context")))
      .child(div().px(16).pb(8).flex_none().text_size(11).text_color(line.tone === "warn" ? t.blocked : t.dim).child(line.text))
      .when(rows.length === 0, (el) => el.child(div().px(16).text_size(12).text_color(t.dim).child("No agents yet. Agents show up here when they report their status to Herdr.")))
      .child(v_flex().id("ir-list").flex_1().min_h(0).px(8).overflow_y_scrollbar().children(rows.map((r, i) => this.row(t, r, i))));
  }
}
