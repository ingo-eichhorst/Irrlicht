import { describe, expect, it } from 'vitest';
import { BIND_WINDOW_MS, PAIR_GAP_MS, TerminalLocator } from '../../src/terminalLocator';
import type { Layout, LocatorTimers, Place } from '../../src/terminalLocator';

// Plain objects stand in for vscode.Tab and vscode.Terminal: the locator only uses their identity.
interface Tab { id: string; label: string }
interface Term { id: string; name: string }

const tab = (id: string, label: string): Tab => ({ id, label });
const term = (id: string, name: string): Term => ({ id, name });

interface GroupSpec { tabs: Tab[]; front?: Tab; active?: boolean }

function layout(groups: GroupSpec[], terminals: Term[], activeTerminal?: Term): Layout<Tab, Term> {
  return {
    groups: groups.map((g) => ({
      terminalTabs: g.tabs.map((t) => ({ tab: t, label: t.label })),
      front: g.front,
      active: g.active ?? false,
    })),
    terminals: terminals.map((t) => ({ terminal: t, name: t.name })),
    activeTerminal,
  };
}

class Clock {
  t = 0;
  now = (): number => this.t;
  advance(ms: number): void { this.t += ms; }
}

/** Timers driven by hand: run() fires every armed callback whose time has come on the shared clock. */
class FakeTimers implements LocatorTimers {
  private next = 0;
  readonly armed = new Map<number, { fn: () => void; at: number }>();
  constructor(private readonly clock: Clock) {}
  set(fn: () => void, ms: number): unknown { const id = ++this.next; this.armed.set(id, { fn, at: this.clock.t + ms }); return id; }
  clear(handle: unknown): void { this.armed.delete(handle as number); }
  run(): void {
    for (const [id, t] of [...this.armed]) if (t.at <= this.clock.t) { this.armed.delete(id); t.fn(); }
  }
}

const placeOf = (places: Map<Term, Place>, t: Term): Place | undefined => places.get(t);
const PANEL: Place = { area: 'panel' };
const UNKNOWN: Place = { area: 'unknown' };
const editor = (group: number, front = true): Place => ({ area: 'editor', group, front });

// The scenario issue #2175 observation 4 is about: two terminals with the same name, one in the panel and
// one in the editor area. The event order is the one recorded with VS Code 1.147 (spike A3: the terminal tab
// opens and becomes the active tab, then onDidChangeActiveTerminal fires for its terminal ~36 ms later).
function sameNameScenario(): { loc: TerminalLocator<Tab, Term>; clock: Clock; p: Term; e: Term; x: Tab; final: Layout<Tab, Term> } {
  const clock = new Clock();
  const loc = new TerminalLocator<Tab, Term>(clock.now);
  const p = term('P', '✳ Claude Code');
  const e = term('E', '✳ Claude Code');
  const x = tab('X', '✳ Claude Code');
  loc.prime(layout([{ tabs: [], active: true }], [p], p));
  // The editor terminal is created: its tab opens and is the active tab before the terminal becomes active.
  loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [p, e], p));
  clock.advance(36);
  loc.observeActiveTerminal(layout([{ tabs: [x], front: x, active: true }], [p, e], e));
  clock.advance(1000);
  // The user then focuses the panel terminal; the editor tab stays the active tab of its group.
  const final = layout([{ tabs: [x], front: x, active: true }], [p, e], p);
  loc.observeActiveTerminal(final);
  return { loc, clock, p, e, x, final };
}

/** Checker: the panel terminal stays in the panel and the editor terminal is in the editor, by identity. */
type LocateAfterSameName = () => { places: Map<Term, Place>; p: Term; e: Term };
function checkSameNameLocated(run: LocateAfterSameName): void {
  const { places, p, e } = run();
  expect(placeOf(places, p), 'panel terminal').toEqual(PANEL);
  expect(placeOf(places, e), 'editor terminal').toEqual(editor(0));
}

describe('TerminalLocator: identity, not label', () => {
  it('two same-named terminals, one in the panel and one in the editor area, are each located (observation 4)', () => {
    checkSameNameLocated(() => {
      const s = sameNameScenario();
      return { places: s.loc.locate(s.final), p: s.p, e: s.e };
    });
  });

  it('a unique name is located by elimination without any learned binding', () => {
    const loc = new TerminalLocator<Tab, Term>(new Clock().now);
    const a = term('A', 'agent');
    const b = term('B', 'zsh');
    const x = tab('X', 'agent');
    const l = layout([{ tabs: [x], front: x, active: true }], [a, b], b);
    loc.prime(l);
    const places = loc.locate(l);
    expect(placeOf(places, a)).toEqual(editor(0));
    expect(placeOf(places, b)).toEqual(PANEL);
  });

  it('an unbound tab whose label several terminals share leaves those terminals unknown, never painted', () => {
    const loc = new TerminalLocator<Tab, Term>(new Clock().now);
    const p = term('P', 'zsh');
    const e = term('E', 'zsh');
    const other = term('O', 'node');
    const x = tab('X', 'zsh');
    const l = layout([{ tabs: [x], front: x, active: true }], [p, e, other], p);
    loc.prime(l);
    const places = loc.locate(l);
    expect(placeOf(places, p)).toEqual(UNKNOWN);
    expect(placeOf(places, e)).toEqual(UNKNOWN);
    expect(placeOf(places, other)).toEqual(PANEL);
  });

  it('startup does not bind a restored tab to whatever terminal happens to be active', () => {
    const clock = new Clock();
    const loc = new TerminalLocator<Tab, Term>(clock.now);
    const p = term('P', 'zsh');
    const e = term('E', 'zsh');
    const x = tab('X', 'zsh');
    const l = layout([{ tabs: [x], front: x, active: true }], [p, e], p);
    loc.prime(l);
    clock.advance(BIND_WINDOW_MS * 4);
    const places = loc.locate(l);
    expect(placeOf(places, p)).toEqual(UNKNOWN);
    expect(placeOf(places, e)).toEqual(UNKNOWN);
  });
});

describe('TerminalLocator: learning from events', () => {
  it('moveToEditor (spike A11): an active-terminal event BEFORE the tab opens does not bind; the one after does', () => {
    const clock = new Clock();
    const loc = new TerminalLocator<Tab, Term>(clock.now);
    const t1 = term('T1', 'zsh');
    const t2 = term('T2', 'zsh');
    loc.prime(layout([{ tabs: [], active: true }], [t1, t2], t1));
    // VS Code first hands the panel's active slot to T2 ...
    loc.observeActiveTerminal(layout([{ tabs: [], active: true }], [t1, t2], t2));
    clock.advance(5);
    // ... then opens T1's editor tab, then makes T1 active.
    const x = tab('X', 'zsh');
    loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [t1, t2], t2));
    clock.advance(3);
    const after = layout([{ tabs: [x], front: x, active: true }], [t1, t2], t1);
    loc.observeActiveTerminal(after);
    const places = loc.locate(after);
    expect(placeOf(places, t1)).toEqual(editor(0));
    expect(placeOf(places, t2)).toEqual(PANEL);
  });

  it('showing a panel terminal while an editor terminal tab is in front (spike A10) learns nothing', () => {
    const s = sameNameScenario();
    // Already covered by the scenario's last step; check the binding survived it.
    expect(placeOf(s.loc.locate(s.final), s.e)).toEqual(editor(0));
    // And with no binding at all, the same event must not create one.
    const clock = new Clock();
    const loc = new TerminalLocator<Tab, Term>(clock.now);
    const p = term('P', 'zsh');
    const e = term('E', 'zsh');
    const x = tab('X', 'zsh');
    loc.prime(layout([{ tabs: [x], front: x, active: true }], [p, e], e));
    clock.advance(BIND_WINDOW_MS * 4);
    const l = layout([{ tabs: [x], front: x, active: true }], [p, e], p);
    loc.observeActiveTerminal(l);
    expect(placeOf(loc.locate(l), p)).toEqual(UNKNOWN);
  });

  it('a tab brought to the front with no active-terminal event binds to the active terminal once the window ends (spike A8)', () => {
    const clock = new Clock();
    const loc = new TerminalLocator<Tab, Term>(clock.now);
    const p = term('P', 'zsh');
    const e = term('E', 'zsh');
    const x = tab('X', 'zsh');
    const readme = tab('R', 'README.md');
    // Restored, unbound: README is in front of the terminal tab.
    loc.prime(layout([{ tabs: [x], front: undefined, active: true }], [p, e], e));
    void readme;
    // README closes: the terminal tab is in front again; its terminal was already the active one, so no event.
    const back = layout([{ tabs: [x], front: x, active: true }], [p, e], e);
    expect(loc.observeTabs(back), 'a binding is pending').toBe(true);
    expect(placeOf(loc.locate(back), e), 'inside the window: not yet known').toEqual(UNKNOWN);
    clock.advance(BIND_WINDOW_MS + 1);
    const places = loc.locate(back);
    expect(placeOf(places, e)).toEqual(editor(0));
    expect(placeOf(places, p)).toEqual(PANEL);
  });

  it('a just-opened tab with no active-terminal change inside the window is not guessed: both stay unknown', () => {
    const clock = new Clock();
    const loc = new TerminalLocator<Tab, Term>(clock.now);
    const p = term('P', 'zsh');
    const e = term('E', 'zsh');
    const x = tab('X', 'zsh');
    loc.prime(layout([{ tabs: [], active: true }], [p, e], e));
    loc.observeActiveTerminal(layout([{ tabs: [], active: true }], [p, e], e));
    loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [p, e], e));
    clock.advance(BIND_WINDOW_MS + 50);
    // No locate ran in between; the late event is the user moving to the panel. Neither the terminal active
    // before (e) nor the late one (p) is evidence for a tab that has just opened.
    const l = layout([{ tabs: [x], front: x, active: true }], [p, e], p);
    loc.observeActiveTerminal(l);
    const places = loc.locate(l);
    expect(placeOf(places, e)).toEqual(UNKNOWN);
    expect(placeOf(places, p)).toEqual(UNKNOWN);
  });

  it('creation: a new tab and a new terminal pair even when the activation arrives after the window (seen under load)', () => {
    const clock = new Clock();
    const loc = new TerminalLocator<Tab, Term>(clock.now);
    const p = term('P', 'e2e-agent');
    const e = term('E', 'e2e-agent');
    const x = tab('X', '');
    loc.prime(layout([{ tabs: [], active: true }], [p], p));
    // The terminal object exists first, then its tab opens with no title yet; P is still the active terminal.
    loc.observeTabs(layout([{ tabs: [], active: true }], [p, e], p));
    clock.advance(40);
    loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [p, e], p));
    clock.advance(BIND_WINDOW_MS * 2);
    x.label = 'e2e-agent';
    const l = layout([{ tabs: [x], front: x, active: true }], [p, e], p);
    const places = loc.locate(l);
    expect(placeOf(places, e)).toEqual(editor(0));
    expect(placeOf(places, p)).toEqual(PANEL);
  });

  it('a tab paired by creation while its activation is pending is not re-bound by a later change in the window', () => {
    const clock = new Clock();
    const loc = new TerminalLocator<Tab, Term>(clock.now);
    const p = term('P', 'zsh');
    const e = term('E', 'zsh');
    const x = tab('X', 'zsh');
    loc.prime(layout([{ tabs: [], active: true }], [p], p));
    // The tab shows up before its terminal: an activation is pending for X ...
    loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [p], p));
    clock.advance(20);
    // ... then the terminal appears and creation pairs it with X ...
    loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [p, e], p));
    clock.advance(20);
    // ... and inside the window the user switches to the panel terminal with the same name. The new terminal
    // next to the new tab contradicts that candidate; creation decides once PAIR_GAP_MS has passed.
    const l = layout([{ tabs: [x], front: x, active: true }], [p, e], p);
    loc.observeActiveTerminal(l);
    clock.advance(PAIR_GAP_MS);
    const places = loc.locate(l);
    expect(placeOf(places, e)).toEqual(editor(0));
    expect(placeOf(places, p)).toEqual(PANEL);
  });

  it('creation does not pair when two tabs or two terminals are new at once, or the names differ', () => {
    const run = (tabsNew: Tab[], termsNew: Term[]): Map<Term, Place> => {
      const clock = new Clock();
      const loc = new TerminalLocator<Tab, Term>(clock.now);
      const p = term('P', 'zsh');
      loc.prime(layout([{ tabs: [], active: true }], [p], p));
      const l = layout([{ tabs: tabsNew, front: undefined, active: true }], [p, ...termsNew], p);
      loc.observeTabs(l);
      return loc.locate(l);
    };
    const two = run([tab('X', 'zsh'), tab('Y', 'zsh')], [term('E', 'zsh')]);
    expect([...two.values()].every((pl) => pl.area !== 'editor'), 'two new tabs').toBe(true);
    const e1 = term('E1', 'zsh');
    const twoTerms = run([tab('X', 'zsh')], [e1, term('E2', 'zsh')]);
    expect(placeOf(twoTerms, e1), 'two new terminals').toEqual(UNKNOWN);
    const a = term('A', 'agent');
    const differ = run([tab('X', 'zsh')], [a]);
    expect(placeOf(differ, a), 'names differ').toEqual(PANEL);
  });

  it('a closed tab (moveToTerminalPanel, spike A12) drops its binding: the terminal is in the panel again', () => {
    const s = sameNameScenario();
    const gone = layout([{ tabs: [], active: true }], [s.p, s.e], s.e);
    s.loc.observeTabs(gone);
    s.loc.observeActiveTerminal(gone);
    const places = s.loc.locate(gone);
    expect(placeOf(places, s.e)).toEqual(PANEL);
    expect(placeOf(places, s.p)).toEqual(PANEL);
  });

  it('a terminal learned for a second tab is no longer bound to the first', () => {
    const s = sameNameScenario();
    // Moving an editor between groups gives it a new tab object in the other group.
    const y = tab('Y', '✳ Claude Code');
    const moved = layout([{ tabs: [], active: false }, { tabs: [y], front: y, active: true }], [s.p, s.e], s.p);
    s.loc.observeTabs(moved);
    s.clock.advance(4);
    const l = layout([{ tabs: [], active: false }, { tabs: [y], front: y, active: true }], [s.p, s.e], s.e);
    s.loc.observeActiveTerminal(l);
    const places = s.loc.locate(l);
    expect(placeOf(places, s.e)).toEqual(editor(1));
    expect(placeOf(places, s.p)).toEqual(PANEL);
  });

  it('review #1: a bound tab back in front never re-binds, even to an active-terminal change inside the window', () => {
    for (const [pName, eName] of [['zsh', 'agent'], ['✳ Claude Code', '✳ Claude Code']]) {
      const clock = new Clock();
      const loc = new TerminalLocator<Tab, Term>(clock.now);
      const p = term('P', pName);
      const e = term('E', eName);
      const x = tab('X', eName);
      loc.prime(layout([{ tabs: [], active: true }], [p], p));
      loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [p, e], p));
      clock.advance(10);
      loc.observeActiveTerminal(layout([{ tabs: [x], front: x, active: true }], [p, e], e));
      clock.advance(1000);
      // README in front of X, then Cmd+W: X is in front again with no event (E was already active) ...
      loc.observeTabs(layout([{ tabs: [x], front: undefined, active: true }], [p, e], e));
      clock.advance(1000);
      loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [p, e], e));
      clock.advance(100);
      // ... and within the window the user focuses the panel terminal.
      const l = layout([{ tabs: [x], front: x, active: true }], [p, e], p);
      loc.observeActiveTerminal(l);
      clock.advance(BIND_WINDOW_MS * 4);
      const places = loc.locate(l);
      expect(placeOf(places, e), `${pName}/${eName}: editor terminal`).toEqual(editor(0));
      expect(placeOf(places, p), `${pName}/${eName}: panel terminal`).toEqual(PANEL);
    }
  });

  it('review #1: a terminal whose name differs from the tab label is never bound to it', () => {
    const clock = new Clock();
    const loc = new TerminalLocator<Tab, Term>(clock.now);
    const p = term('P', 'zsh');
    const a = term('A', 'agent');
    const x = tab('X', 'agent');
    loc.prime(layout([{ tabs: [x], front: undefined, active: true }], [p, a], p));
    loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [p, a], p));
    clock.advance(10);
    const l = layout([{ tabs: [x], front: x, active: true }], [p, a], p);
    loc.observeActiveTerminal(l);
    clock.advance(BIND_WINDOW_MS * 4);
    const places = loc.locate(l);
    expect(placeOf(places, a)).toEqual(editor(0));
    expect(placeOf(places, p)).toEqual(PANEL);
  });

  it('a name that differs only for a moment inside the window is retried when the window ends', () => {
    const clock = new Clock();
    const loc = new TerminalLocator<Tab, Term>(clock.now);
    const p = term('P', 'zsh');
    const e = term('E', '');
    const x = tab('X', 'zsh');
    loc.prime(layout([{ tabs: [], active: true }], [p], p));
    loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [p, e], p));
    clock.advance(10);
    // The new terminal's name has not arrived yet when it becomes active.
    loc.observeActiveTerminal(layout([{ tabs: [x], front: x, active: true }], [p, e], e));
    e.name = 'zsh';
    clock.advance(BIND_WINDOW_MS + 1);
    const l = layout([{ tabs: [x], front: x, active: true }], [p, e], e);
    const places = loc.locate(l);
    expect(placeOf(places, e)).toEqual(editor(0));
    expect(placeOf(places, p)).toEqual(PANEL);
  });

  it('transfer: an editor moved to another group (old tab closes, new tab opens, no event) keeps its terminal at once', () => {
    const s = sameNameScenario();
    const y = tab('Y', '✳ Claude Code');
    // The moved terminal is the active one already; VS Code fires no active-terminal change.
    const moved = layout([{ tabs: [], active: false }, { tabs: [y], front: y, active: true }], [s.p, s.e], s.e);
    expect(s.loc.observeTabs(moved), 'nothing left to learn').toBe(false);
    const places = s.loc.locate(moved);
    expect(placeOf(places, s.e)).toEqual(editor(1));
    expect(placeOf(places, s.p)).toEqual(PANEL);
  });

  it('transfer does not guess between two new tabs with the released terminal\'s label', () => {
    const s = sameNameScenario();
    const y = tab('Y', '✳ Claude Code');
    const z = tab('Z', '✳ Claude Code');
    const l = layout([{ tabs: [y], front: y, active: false }, { tabs: [z], front: z, active: false }], [s.p, s.e], s.e);
    s.loc.observeTabs(l);
    const places = s.loc.locate(l);
    expect(placeOf(places, s.e)).toEqual(UNKNOWN);
  });

  it('a pending binding with no further event still settles: onDue fires once the window has passed', () => {
    const clock = new Clock();
    const timers = new FakeTimers(clock);
    let due = 0;
    const loc = new TerminalLocator<Tab, Term>(clock.now, BIND_WINDOW_MS, { timers, onDue: () => { due++; } });
    const p = term('P', 'zsh');
    const e = term('E', 'zsh');
    const x = tab('X', 'zsh');
    loc.prime(layout([{ tabs: [x], front: undefined, active: true }], [p, e], e));
    const back = layout([{ tabs: [x], front: x, active: true }], [p, e], e);
    loc.observeTabs(back);
    clock.advance(BIND_WINDOW_MS + 49);
    timers.run();
    expect(due, 'not before the window and its margin have passed (review 2 F3: +1 ms raced Date.now)').toBe(0);
    clock.advance(1);
    timers.run();
    expect(due, 'exactly once after it').toBe(1);
    expect(placeOf(loc.locate(back), e)).toEqual(editor(0));
  });

  it('a due callback that leaves the binding undecided looks again, until CONFIRM_MAX_MS', () => {
    const clock = new Clock();
    const timers = new FakeTimers(clock);
    let due = 0;
    let loc: TerminalLocator<Tab, Term> | undefined;
    const p = term('P', 'zsh');
    const e = term('E', 'zsh');
    const x = tab('X', '');
    loc = new TerminalLocator<Tab, Term>(clock.now, BIND_WINDOW_MS, { timers, onDue: () => { due++; loc?.locate(l()); } });
    // A restored tab whose title has not arrived: the candidate waits for it.
    const l = (): Layout<Tab, Term> => layout([{ tabs: [x], front: x, active: true }], [p, e], e);
    loc.prime(layout([{ tabs: [x], front: undefined, active: true }], [p, e], e));
    loc.observeTabs(l());
    for (let i = 0; i < 4; i++) { clock.advance(BIND_WINDOW_MS + 50); timers.run(); }
    expect(due, 'it kept looking while the title was missing').toBe(4);
    x.label = 'zsh';
    clock.advance(BIND_WINDOW_MS + 50);
    timers.run();
    expect(placeOf(loc.locate(l()), e)).toEqual(editor(0));
    clock.advance(10_000);
    timers.run();
    expect(timers.armed.size, 'decided: nothing left armed').toBe(0);
  });

  it('review 2 F1: a new panel terminal shortly before a terminal is moved to the editor is not paired with its tab', () => {
    const clock = new Clock();
    const loc = new TerminalLocator<Tab, Term>(clock.now);
    const e = term('E', 'zsh');
    loc.prime(layout([{ tabs: [], active: true }], [e], e));
    // Ctrl+Shift+` opens N in the panel; it becomes active.
    const n = term('N', 'zsh');
    loc.observeTabs(layout([{ tabs: [], active: true }], [e, n], n));
    loc.observeActiveTerminal(layout([{ tabs: [], active: true }], [e, n], n));
    clock.advance(600);
    // E moves to the editor: its tab opens, then E becomes active.
    const x = tab('X', 'zsh');
    loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [e, n], n));
    clock.advance(30);
    loc.observeActiveTerminal(layout([{ tabs: [x], front: x, active: true }], [e, n], e));
    clock.advance(2000);
    const places = loc.locate(layout([{ tabs: [x], front: x, active: true }], [e, n], e));
    expect(placeOf(places, e)).toEqual(editor(0));
    expect(placeOf(places, n)).toEqual(PANEL);
  });

  it('review 2 F1: two new terminals around a new tab make creation ambiguous; activation decides', () => {
    const clock = new Clock();
    const loc = new TerminalLocator<Tab, Term>(clock.now);
    const p = term('P', 'zsh');
    loc.prime(layout([{ tabs: [], active: true }], [p], p));
    const n = term('N', 'zsh');
    loc.observeTabs(layout([{ tabs: [], active: true }], [p, n], n));
    clock.advance(690);
    // "Create New Terminal in Editor Area": the tab is seen before its terminal.
    const x = tab('X', 'zsh');
    loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [p, n], n));
    clock.advance(10);
    const e = term('E', 'zsh');
    loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [p, n, e], n));
    clock.advance(20);
    loc.observeActiveTerminal(layout([{ tabs: [x], front: x, active: true }], [p, n, e], e));
    clock.advance(2000);
    const places = loc.locate(layout([{ tabs: [x], front: x, active: true }], [p, n, e], e));
    expect(placeOf(places, e)).toEqual(editor(0));
    expect(placeOf(places, n)).toEqual(PANEL);
  });

  it('review 2 F2: activation never takes a terminal another live tab already shows', () => {
    const s = sameNameScenario();
    // A restored, unlearned tab Z in another group with the same title comes to the front; inside the window
    // the user clicks into E (shown by X).
    const z = tab('Z', '✳ Claude Code');
    const f = term('F', '✳ Claude Code');
    s.loc.prime(layout([{ tabs: [s.x], front: s.x, active: false }, { tabs: [z], front: undefined, active: true }], [s.p, s.e, f], s.p));
    s.loc.observeTabs(layout([{ tabs: [s.x], front: s.x, active: false }, { tabs: [z], front: z, active: true }], [s.p, s.e, f], s.p));
    s.clock.advance(50);
    const l = layout([{ tabs: [s.x], front: s.x, active: false }, { tabs: [z], front: z, active: true }], [s.p, s.e, f], s.e);
    s.loc.observeActiveTerminal(l);
    s.clock.advance(2000);
    const places = s.loc.locate(l);
    expect(placeOf(places, s.e), 'E stays with X').toEqual(editor(0));
  });

  it('review 2 F4: a late change with no previous active terminal binds nothing', () => {
    const clock = new Clock();
    const loc = new TerminalLocator<Tab, Term>(clock.now);
    const p = term('P', 'zsh');
    const e = term('E', 'zsh');
    const x = tab('X', 'zsh');
    loc.prime(layout([{ tabs: [x], front: undefined, active: true }], [p, e], undefined));
    loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [p, e], undefined));
    clock.advance(BIND_WINDOW_MS + 100);
    const l = layout([{ tabs: [x], front: x, active: true }], [p, e], p);
    loc.observeActiveTerminal(l);
    const places = loc.locate(l);
    expect(placeOf(places, p)).toEqual(UNKNOWN);
    expect(placeOf(places, e)).toEqual(UNKNOWN);
  });

  it('review 2 F5: a terminal released by moveToTerminalPanel is not handed to a tab opened 700 ms later', () => {
    const s = sameNameScenario();
    // E goes back to the panel: X closes.
    const back = layout([{ tabs: [], active: true }], [s.p, s.e], s.e);
    s.loc.observeTabs(back);
    s.clock.advance(700);
    // P is moved to the editor.
    const y = tab('Y', '✳ Claude Code');
    s.loc.observeTabs(layout([{ tabs: [y], front: y, active: true }], [s.p, s.e], s.e));
    s.clock.advance(5);
    const l = layout([{ tabs: [y], front: y, active: true }], [s.p, s.e], s.p);
    s.loc.observeActiveTerminal(l);
    s.clock.advance(2000);
    const places = s.loc.locate(l);
    expect(placeOf(places, s.p)).toEqual(editor(0));
    expect(placeOf(places, s.e)).toEqual(PANEL);
  });

  it('review 3: closing the active editor terminal does not hand the tab behind it to the panel fallback', () => {
    const clock = new Clock();
    const loc = new TerminalLocator<Tab, Term>(clock.now);
    const p = term('P', '✳ Claude Code');
    const b = term('B', '✳ Claude Code');
    const t = term('T', 'zsh');
    const y = tab('Y', '✳ Claude Code');
    const tt = tab('TT', 'zsh');
    // Restored: Y behind TT in one group, P in the panel (the panel's last active terminal), T active.
    loc.prime(layout([{ tabs: [y, tt], front: tt, active: true }], [p, b, t], t));
    // Ctrl+W on TT: Y comes to the front and TT closes, T closes, VS Code falls back to P, then activates B.
    loc.observeTabs(layout([{ tabs: [y], front: y, active: true }], [p, b, t], t));
    clock.advance(1);
    loc.locate(layout([{ tabs: [y], front: y, active: true }], [p, b], t));
    clock.advance(1);
    loc.observeActiveTerminal(layout([{ tabs: [y], front: y, active: true }], [p, b], p));
    clock.advance(4);
    const l = layout([{ tabs: [y], front: y, active: true }], [p, b], b);
    loc.observeActiveTerminal(l);
    clock.advance(2000);
    const places = loc.locate(l);
    expect(placeOf(places, p)?.area, 'the panel terminal is never placed in the editor').not.toBe('editor');
    expect(placeOf(places, b)?.area, 'the editor terminal is never placed in the panel').not.toBe('panel');
  });

  it('review 2 F6: an orphaned tab is never paired with a new terminal', () => {
    const s = sameNameScenario();
    // E closes while its tab is still listed: X is orphaned. A new panel terminal opens right away.
    const n = term('N', '✳ Claude Code');
    s.loc.observeTabs(layout([{ tabs: [s.x], front: s.x, active: true }], [s.p], s.p));
    s.loc.observeTabs(layout([{ tabs: [s.x], front: s.x, active: true }], [s.p, n], n));
    s.clock.advance(2000);
    const places = s.loc.locate(layout([{ tabs: [s.x], front: s.x, active: true }], [s.p, n], n));
    expect(placeOf(places, n)).toEqual(PANEL);
  });

  it('a pending binding that is resolved or superseded does not leave a stale callback', () => {
    const clock = new Clock();
    const timers = new FakeTimers(clock);
    const loc = new TerminalLocator<Tab, Term>(clock.now, BIND_WINDOW_MS, { timers, onDue: () => undefined });
    const p = term('P', 'zsh');
    const x = tab('X', 'zsh');
    loc.prime(layout([{ tabs: [x], front: undefined, active: true }], [p], p));
    loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [p], p));
    expect(timers.armed.size).toBe(1);
    loc.observeTabs(layout([{ tabs: [x], front: undefined, active: true }], [p], p));
    expect(timers.armed.size, 'the tab left the front: nothing pending').toBe(0);
    loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [p], p));
    loc.dispose();
    expect(timers.armed.size, 'dispose cancels it').toBe(0);
  });

  it('a bound terminal whose tab is not in front of its group is located with front=false', () => {
    const s = sameNameScenario();
    const readme = layout([{ tabs: [s.x], front: undefined, active: true }], [s.p, s.e], s.p);
    s.loc.observeTabs(readme);
    expect(placeOf(s.loc.locate(readme), s.e)).toEqual(editor(0, false));
  });

  it('a closed terminal is forgotten', () => {
    const s = sameNameScenario();
    const l = layout([{ tabs: [s.x], front: s.x, active: true }], [s.p], s.p);
    s.loc.observeTabs(l);
    const places = s.loc.locate(l);
    expect(places.has(s.e)).toBe(false);
    // The tab now has a unique-label candidate again, but that candidate is the panel terminal: the
    // locator must not move it into the editor by elimination while the tab's own terminal is gone.
    expect(placeOf(places, s.p)).not.toEqual(editor(0));
  });
});

// ---------------------------------------------------------------------------
// Committed mutant: the locator the extension shipped before #2175, which put a terminal in the editor
// area whenever any editor terminal tab carried its name. The checker must reject it.
// ---------------------------------------------------------------------------
function labelLocate(l: Layout<Tab, Term>): Map<Term, Place> {
  const out = new Map<Term, Place>();
  for (const { terminal, name } of l.terminals) {
    const gi = l.groups.findIndex((g) => g.terminalTabs.some((t) => t.label === name));
    out.set(terminal, gi < 0 ? PANEL : editor(gi));
  }
  return out;
}

describe('committed locator mutant', () => {
  it('the label-matching locator is caught by the same-name checker', () => {
    expect(() => checkSameNameLocated(() => {
      const s = sameNameScenario();
      return { places: labelLocate(s.final), p: s.p, e: s.e };
    })).toThrow();
  });
});
