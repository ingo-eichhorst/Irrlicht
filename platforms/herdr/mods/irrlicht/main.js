// Irrlicht: Herdr's agents as the macOS Irrlicht app lists sessions, each with
// its Irrlicht state, model and context fill, read from the local Irrlicht
// daemon (GET /api/v1/sessions). Read-only: it writes no file and sends the
// daemon nothing but that GET. The only programs it runs are
// /usr/bin/printenv (twice, at start, for IRRLICHT_BIND_ADDR and
// IRRLICHT_HOME: a mod has no process.env) and /bin/sh's `[ -f ] && [ ! -L ]`
// test of the addr file (a mod has no lstat), at start and at most every 30 s
// while the daemon does not answer (lib/poll.js RERESOLVE_MS). The decisions
// live in ./lib, which imports nothing from Herdr; this file only reads Herdr,
// starts the timer and draws. The look follows the Swift app's
// SessionListView, GroupView, SessionRowView and SessionStateIcon (in
// platforms/macos/Irrlicht/Views): Irrlicht's hues from lib/palette.js, and
// Herdr's theme() for everything neutral.

import { View, div, image, svg } from "gpui-kit";
import { v_flex, h_flex } from "gpui-base";
import { agents, terminals, theme, run, exec, home, readTextFile, checking, nextCommand } from "herdr";
import { headerSummary, panelLayout } from "./lib/layout.js";
import { BRAND, connectionHue, track, wash } from "./lib/palette.js";
import { createPoller } from "./lib/poll.js";
import { focusAddress, panelRows, signature, statusLine } from "./lib/rows.js";
import { STATE_HUE } from "./lib/states.js";

const POLL_MS = 3000;
// Figures, model names and group names are monospaced in the Swift app; Geist
// Mono ships with Herdr (its AGENTS.md, "The look").
const MONO = "Geist Mono";
// Icons are found from Herdr's home folder (~/.config/herdr-app), where this
// mod is installed as mods/irrlicht.
const ICONS = "mods/irrlicht/icons/";
// Where a row's second line starts: under the title, past the 12px state icon
// and the 12px number and the 6px gap after each (SessionRowView.swift:300-317).
const LEAD = 36;

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

// programIcon is the agent's brand mark from Herdr's program table, where the
// Swift row puts its adapter icon (SessionRowView.swift:477-481).
function programIcon(p, color) {
  if (!p?.icon) return null;
  return p.tint ? svg(p.icon).size(12).flex_none().text_color(color) : image(p.icon).w(12).h(12).flex_none();
}

function focusAgent(address) {
  run("pane.focus", { terminal: address });
}

function glyph(name, hue, size) {
  return svg(ICONS + name + ".svg").size(size).flex_none().text_color(hue);
}

// pauseBar is one bar of the waiting glyph, in SessionStateIcon.swift's
// proportions (lines 99-121): 2.5/16 wide, 10/16 tall.
function pauseBar(hue, size) {
  return div().w((size * 2.5) / 16).h((size * 10) / 16).rounded(size / 16).bg(hue);
}

// STATE_ICON draws each state as SessionStateIcon does (SessionStateIcon.swift):
// a solid dot that breathes between full and 0.55 opacity over 1.4 s, two
// pause bars, and the disc-check, disc-mark and ring-question glyphs.
const STATE_ICON = {
  working: (hue, size) => div().size(size).flex_none().rounded(size / 2).bg(hue).loop("opacity", { from: 1, to: 0.55, period: 1400 }),
  waiting: (hue, size) => h_flex().size(size).flex_none().items_center().justify_center().gap((size * 3) / 16).child(pauseBar(hue, size)).child(pauseBar(hue, size)),
  ready: (hue, size) => glyph("state-ready", hue, size),
  error: (hue, size) => glyph("state-error", hue, size),
  unknown: (hue, size) => glyph("state-unknown", hue, size),
};

function stateIcon(state) {
  const draw = STATE_ICON[state] ?? STATE_ICON.unknown;
  return draw(STATE_HUE[state] ?? STATE_HUE.unknown, 12);
}

// subagentBadge is the Swift row's count of working and waiting subagents: a
// 14px disc in the working hue with the count in white
// (SessionRowView.swift:361-370).
function subagentBadge(r) {
  const n = r.subagents;
  return div().id(`ir-sub-${r.address}`).size(14).flex_none().rounded(7).bg(BRAND.working)
    .flex().items_center().justify_center()
    .text_size(9).font_bold().text_color("#FFFFFF")
    .tooltip(`${n} active subagent${n === 1 ? "" : "s"}`)
    .child(String(n));
}

// noteText is a note's words in the theme's text colour on the hue's wash: the
// question wraps to at most three lines (SessionRowView.swift:149, lineLimit
// 3), the other notes keep to one.
function noteText(t, note, question) {
  const text = div().flex_1().min_w(0).text_size(10.5).text_color(t.foreground);
  if (question) return text.line_height(1.3).max_h(41).overflow_hidden().child(note.text);
  return text.truncate().child(note.text);
}

function mono(size, color) {
  return div().text_size(size).font_family(MONO).text_color(color);
}

export default class Irrlicht extends View {
  init(_props, cx) {
    this.state = { phase: "starting" };
    // The order groups were first shown in (lib/layout.js rememberGroups),
    // and the groups the person folded.
    this.remembered = [];
    this.collapsed = new Set();
    this.sig = signature(this.state, null);
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
    // focuses the agent in row n as drawn (from 1), exactly as clicking that
    // row does. Each turn uses its `cx`: Herdr's AGENTS.md says a reloaded
    // build's old loop "ends at its next `cx` use", so without one the old
    // loop would keep waiting beside the new build's.
    cx.spawn(async (cx) => {
      while (!this.closed) {
        this.command(await nextCommand()); // NOSONAR (javascript:S9382): commands arrive one at a time; waiting for the next is the loop
        cx.notify();
      }
    });
  }

  // command runs one mod command: "focus-<n>" focuses row n as drawn now.
  command(name) {
    const address = focusAddress(name, this.layout().rows);
    if (address) focusAgent(address);
  }

  // close is Herdr's last call to a copy. Herdr cancels the copy's pending
  // calls then, so the loop above stops at its `await`; the flag only keeps
  // it from asking again should that await ever return.
  async close() {
    this.closed = true;
  }

  tick(cx) {
    const step = this.poller.step();
    if (step.kind === "stalled") this.show(cx, step.state);
    if (step.kind === "request") cx.spawn(async (cx) => this.show(cx, await step.request));
  }

  // layout is the rows as drawn now: grouped and ordered by lib/layout.js,
  // which also keeps the remembered group order. Without sessions no row is
  // matched, so the terminals need not be read.
  layout() {
    const sessions = this.state.sessions ?? null;
    const laid = panelLayout(panelRows(agents(), sessions ? terminals() : [], sessions), this.remembered);
    this.remembered = laid.remembered;
    return laid;
  }

  // show adopts a state and redraws only when what is drawn changed.
  show(cx, next) {
    this.state = next;
    const sig = signature(next, this.layout());
    if (sig !== this.sig) {
      this.sig = sig;
      cx.notify();
    }
  }

  toggle(cx, key) {
    if (this.collapsed.has(key)) this.collapsed.delete(key);
    else this.collapsed.add(key);
    cx.notify();
  }

  // header is the Swift list's header (SessionListView.swift:494-555):
  // "Irrlicht", the rows' state icons (or a count), and the connection dot,
  // whose tooltip says which daemon the panel reads.
  header(t, layout, line) {
    return h_flex().px(12).h(36).flex_none().gap(6).items_center()
      .child(div().text_size(13).font_semibold().text_color(t.foreground).child("Irrlicht"))
      .child(this.summary(t, headerSummary(layout)))
      .child(div().flex_1())
      .child(div().id("ir-connection").size(6).flex_none().rounded(3).bg(connectionHue(this.state.phase)).tooltip(line.text));
  }

  summary(t, s) {
    const any = Boolean(s.text) || s.states.length > 0;
    return h_flex().gap(4).items_center()
      .when(any, (el) => el.child(div().text_color(t.dim).child("—")))
      .when(Boolean(s.text), (el) => el.child(mono(11, t.muted).child(s.text)))
      .children(s.states.map((state) => stateIcon(state)));
  }

  // statusRow is shown while the daemon is not answering, where the Swift app
  // puts its daemon banner: above the list. A fault inside the panel or a
  // foreign answer sits on the error wash.
  statusRow(t, line) {
    const warn = line.tone === "warn";
    return div().mx(12).mb(6).px(6).py(2).rounded(4)
      .when(warn, (el) => el.bg(wash(BRAND.error)))
      .text_size(11).text_color(warn ? t.foreground : t.dim).child(line.text);
  }

  // groupHeader is GroupView's header (GroupView.swift:67-141): the fold
  // chevron, the Gas Town mark on that orchestrator's group, the name in
  // monospace, and the count on the right.
  groupHeader(t, g, i) {
    const open = !this.collapsed.has(g.key);
    return h_flex().id(`ir-group-${i}`).px(12).pt(8).pb(3).flex_none().gap(6).items_center().cursor_pointer()
      .tooltip(open ? "Collapse group" : "Expand group")
      .on_click((_e, cx) => this.toggle(cx, g.key))
      .child(glyph(open ? "chevron-down" : "chevron-right", t.dim, 10))
      .when(g.gastown, (el) => el.child(div().text_size(10).child("⛽")))
      .child(mono(11.5, t.foreground).min_w(0).truncate().font_semibold().child(g.name))
      .child(div().flex_1())
      .child(div().flex_none().text_size(10.5).text_color(t.dim).child(g.count));
  }

  group(t, g, i) {
    const header = this.groupHeader(t, g, i);
    if (this.collapsed.has(g.key)) return [header];
    return [header, ...g.rows.map((r) => this.row(t, r))];
  }

  // row is SessionRowView (SessionRowView.swift:298-574) in a 320px column: its
  // main line, then the context bar on a line of its own, then the notes.
  row(t, r) {
    return v_flex()
      .id(`ir-${r.address}`)
      .ml(8).mr(4).px(10).py(4).gap(2).flex_none().rounded(6)
      .when(r.focused, (el) => el.bg(t.selected))
      .cursor_pointer()
      .hover((s) => s.bg(t.hover))
      .tooltip(`${r.n}. ${r.tooltip}`)
      .on_click(() => focusAgent(r.address))
      .child(this.headline(t, r))
      .when(r.gauge.kind !== "none", (el) => el.child(this.contextLine(t, r.gauge)))
      .children(r.notes.map((note) => this.note(t, r, note)));
  }

  // headline: state icon, number, subagent badge, title, model, program icon.
  headline(t, r) {
    const pic = programIcon(r.program, t.foreground);
    return h_flex().h(18).gap(6).items_center()
      .child(div().id(`ir-state-${r.address}`).flex_none().tooltip(r.stateTip).child(stateIcon(r.state)))
      .child(mono(9.5, t.dim).w(12).flex_none().text_right().child(String(r.n)))
      .when(r.subagents > 0, (el) => el.child(subagentBadge(r)))
      .child(div().flex_1().min_w(0).truncate().text_size(12.5).text_color(t.foreground).child(r.label))
      .when(Boolean(r.model), (el) => el.child(mono(10.5, t.muted).flex_none().max_w(96).truncate().child(r.model)))
      .when(Boolean(pic), (el) => el.child(pic));
  }

  // contextLine is the Swift row's context columns: the bar with the token
  // count inside it and the percentage beside it, both in the pressure hue;
  // or the token text and a dash when the window is unknown.
  contextLine(t, g) {
    const percent = g.kind === "percent";
    return h_flex().h(15).pl(LEAD).gap(6).items_center()
      .child(percent ? this.bar(t, g) : mono(10, t.muted).flex_1().min_w(0).truncate().child(g.text))
      .child(mono(10, percent ? g.hue : t.muted).w(38).flex_none().child(percent ? g.text : "—"));
  }

  // bar is ContextBar (SessionRowView.swift:6-29): a 13px track, the fill in
  // the pressure hue, and the token count right-aligned on top.
  bar(t, g) {
    const fill = `${Math.round(g.fraction * 1000) / 10}%`;
    return div().flex_1().min_w(0).h(13).relative().overflow_hidden().rounded(3).bg(track(t.foreground))
      .child(div().absolute().left(0).top(0).h(13).w(fill).rounded(3).bg(g.hue))
      .child(h_flex().absolute().right(4).top(0).h(13).items_center().child(mono(8.5, t.muted).child(g.label)));
  }

  // note is one line under a row (lib/notes.js) on its hue's wash: the error
  // line and the pressure alert after a warning triangle, the waiting
  // question as a pill of up to three lines.
  note(t, r, note) {
    const question = note.kind === "question";
    return h_flex().id(`ir-${note.kind}-${r.address}`).px(6).py(2).gap(4).items_start().rounded(4).bg(wash(note.hue))
      .tooltip(note.tooltip)
      .when(!question, (el) => el.child(glyph("alert", note.hue, 10).mt(2)))
      .child(noteText(t, note, question));
  }

  empty(t) {
    return div().px(12).pt(10).text_size(12).text_color(t.dim)
      .child("No agents yet. Agents show up here when they report their status to Herdr.");
  }

  render() {
    const t = theme();
    const layout = this.layout();
    const line = statusLine(this.state);
    return v_flex()
      .size_full()
      .min_h(0)
      .bg(t.chrome)
      .text_size(13)
      .child(this.header(t, layout, line))
      .when(this.state.phase !== "ok", (el) => el.child(this.statusRow(t, line)))
      .child(div().h(1).flex_none().bg(t.border))
      .when(layout.rows.length === 0, (el) => el.child(this.empty(t)))
      .child(v_flex().id("ir-list").flex_1().min_h(0).py(4).overflow_y_scrollbar()
        .children(layout.groups.flatMap((g, i) => this.group(t, g, i))));
  }
}
