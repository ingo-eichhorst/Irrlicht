import { describe, expect, it } from 'vitest';
import { BIND_WINDOW_MS, TerminalLocator } from '../../src/terminalLocator';
import type { Layout, Place } from '../../src/terminalLocator';

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

  it('an active-terminal event after the window does not bind the pending tab to the new terminal', () => {
    const clock = new Clock();
    const loc = new TerminalLocator<Tab, Term>(clock.now);
    const p = term('P', 'zsh');
    const e = term('E', 'zsh');
    const x = tab('X', 'zsh');
    loc.prime(layout([{ tabs: [], active: true }], [p, e], e));
    loc.observeActiveTerminal(layout([{ tabs: [], active: true }], [p, e], e));
    loc.observeTabs(layout([{ tabs: [x], front: x, active: true }], [p, e], e));
    clock.advance(BIND_WINDOW_MS + 50);
    // No locate ran in between; the late event is the user moving to the panel.
    const l = layout([{ tabs: [x], front: x, active: true }], [p, e], p);
    loc.observeActiveTerminal(l);
    const places = loc.locate(l);
    expect(placeOf(places, e)).toEqual(editor(0));
    expect(placeOf(places, p)).toEqual(PANEL);
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
