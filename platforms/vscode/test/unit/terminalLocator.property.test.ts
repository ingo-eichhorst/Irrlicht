// Property test for TerminalLocator: random sequences of user actions against a model of VS Code's terminal
// and editor-tab state, emitting events in the order the #2175 spike recorded with VS Code 1.147. Property:
// the locator never places a terminal in an area it is not in; `unknown` is always allowed.
//
// Timing is kept to what the locator's header documents as decidable: activation events follow their tab
// change within a few ms, and user actions are at least ACTION_GAP_MS apart (more than BIND_WINDOW_MS,
// PAIR_GAP_MS and the due margin). Faster actions are a documented limitation, not checked here.
import { describe, expect, it } from 'vitest';
import { BIND_WINDOW_MS, TerminalLocator } from '../../src/terminalLocator';
import type { Layout, LocatorTimers, Place } from '../../src/terminalLocator';

const ACTION_GAP_MS = 400;
const NAMES = ['zsh', '✳ Claude Code'];

interface Term { id: number; name: string }
interface Tab { id: number; term?: Term; label: string }
interface Group { tabs: Tab[]; front: Tab | undefined }

function rng(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6D2B79F5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

class Timers implements LocatorTimers {
  private next = 0;
  private readonly armed = new Map<number, { fn: () => void; at: number }>();
  constructor(private readonly now: () => number) {}
  set(fn: () => void, ms: number): unknown { const id = ++this.next; this.armed.set(id, { fn, at: this.now() + ms }); return id; }
  clear(h: unknown): void { this.armed.delete(h as number); }
  run(): void {
    for (const [id, t] of [...this.armed].sort((a, b) => a[1].at - b[1].at)) {
      if (t.at <= this.now() && this.armed.has(id)) { this.armed.delete(id); t.fn(); }
    }
  }
}

class World {
  t = 0;
  ids = 0;
  terms: Term[] = [];
  groups: Group[] = [{ tabs: [], front: undefined }];
  activeGroup = 0;
  active: Term | undefined;
  /** The panel's last active terminal: VS Code falls back to it when the active editor terminal goes away. */
  lastPanelActive: Term | undefined;
  readonly log: string[] = [];
  readonly timers = new Timers(() => this.t);
  readonly loc: TerminalLocator<Tab, Term>;

  constructor(readonly r: () => number) {
    this.loc = new TerminalLocator<Tab, Term>(() => this.t, BIND_WINDOW_MS, { timers: this.timers, onDue: () => this.check('due') });
  }

  pick<T>(xs: T[]): T { return xs[Math.floor(this.r() * xs.length)]; }
  newTerm(): Term { return { id: ++this.ids, name: this.pick(NAMES) }; }
  tabOf(t: Term): { g: number; tab: Tab } | undefined {
    for (let g = 0; g < this.groups.length; g++) {
      const tab = this.groups[g].tabs.find((x) => x.term === t);
      if (tab) return { g, tab };
    }
    return undefined;
  }
  panelTerms(): Term[] { return this.terms.filter((t) => !this.tabOf(t)); }
  editorTerms(): Term[] { return this.terms.filter((t) => this.tabOf(t)); }

  layout(): Layout<Tab, Term> {
    return {
      groups: this.groups.map((g, i) => ({
        terminalTabs: g.tabs.filter((x) => x.term).map((x) => ({ tab: x, label: x.label })),
        front: g.front?.term ? g.front : undefined,
        active: i === this.activeGroup,
      })),
      terminals: this.terms.map((t) => ({ terminal: t, name: t.name })),
      activeTerminal: this.active,
    };
  }

  /** The property, checked after every event the controller would turn into a locate(). */
  check(when: string): void {
    const places = this.loc.locate(this.layout());
    for (const t of this.terms) {
      const got = places.get(t);
      const at = this.tabOf(t);
      const want: Place = at ? { area: 'editor', group: at.g, front: this.groups[at.g].front === at.tab } : { area: 'panel' };
      if (got === undefined || got.area === 'unknown') continue;
      const describe = (x: Tab | Term): string => ('label' in x ? `tab${x.id}` : `term${x.id}`);
      const state = JSON.stringify(this.loc.debugState(describe));
      expect(got, `terminal ${t.id} (${t.name}) after ${when}\n${this.log.join('\n')}\nlocator: ${state}`).toEqual(want);
    }
  }

  tick(ms: number): void { this.t += ms; this.timers.run(); }
  emitTabs(what: string): void { this.log.push(`${this.t} tabs: ${what}`); this.loc.observeTabs(this.layout()); this.check(what); this.tick(1 + Math.floor(this.r() * 5)); }
  emitTerms(what: string): void { this.log.push(`${this.t} terms: ${what}`); this.check(what); this.tick(1 + Math.floor(this.r() * 5)); }
  setActive(t: Term | undefined, why: string): void {
    if (t !== undefined && !this.tabOf(t)) this.lastPanelActive = t;
    if (t === this.active) return;
    this.active = t;
    this.log.push(`${this.t} active -> ${t ? t.id : 'none'} (${why})`);
    this.loc.observeActiveTerminal(this.layout());
    this.check(`active ${why}`);
    this.tick(1 + Math.floor(this.r() * 5));
  }
  activeFront(): Tab | undefined { return this.groups[this.activeGroup]?.front; }
  /**
   * Applies a change to the tab model and emits it. When the active group's front tab changed to a terminal
   * tab, VS Code makes that terminal the active one (terminalEditorService on onDidActiveEditorChange).
   */
  change(why: string, fn: () => void): void {
    const before = this.activeFront();
    fn();
    const after = this.activeFront();
    this.emitTabs(why);
    if (after !== before && after?.term) this.setActive(after.term, `front ${why}`);
  }
  dropEmptyGroup(g: number): void {
    if (this.groups.length <= 1 || this.groups[g].tabs.length > 0) return;
    this.groups.splice(g, 1);
    if (this.activeGroup >= g && this.activeGroup > 0) this.activeGroup--;
  }
  addTab(g: number, tab: Tab): void { this.groups[g].tabs.push(tab); this.groups[g].front = tab; }
  removeTab(g: number, tab: Tab): void {
    const grp = this.groups[g];
    grp.tabs = grp.tabs.filter((x) => x !== tab);
    if (grp.front === tab) grp.front = grp.tabs[grp.tabs.length - 1];
  }
}

type Action = (w: World) => boolean;

const ACTIONS: Record<string, Action> = {
  createPanel: (w) => {
    const t = w.newTerm();
    w.terms.push(t);
    w.emitTerms(`create panel ${t.id}`);
    w.setActive(t, 'created');
    return true;
  },
  createEditor: (w) => {
    const t = w.newTerm();
    const tab: Tab = { id: ++w.ids, term: t, label: w.r() < 0.5 ? '' : t.name };
    const termFirst = w.r() < 0.7;
    if (termFirst) { w.terms.push(t); w.emitTerms(`create editor ${t.id}`); }
    w.change(`open tab ${tab.id} for ${t.id}`, () => w.addTab(w.activeGroup, tab));
    if (!termFirst) { w.terms.push(t); w.emitTerms(`create editor ${t.id} (after tab)`); }
    if (tab.label === '') { tab.label = t.name; w.emitTabs(`title ${tab.id}`); }
    w.setActive(t, 'created in editor');
    return true;
  },
  moveToEditor: (w) => {
    const panel = w.panelTerms();
    if (panel.length === 0) return false;
    const t = w.pick(panel);
    const others = panel.filter((x) => x !== t);
    if (others.length > 0 && w.r() < 0.5) w.setActive(w.pick(others), 'transient');
    const tab: Tab = { id: ++w.ids, term: t, label: t.name };
    w.change(`moveToEditor ${t.id} tab ${tab.id}`, () => w.addTab(w.activeGroup, tab));
    w.setActive(t, 'moved to editor');
    return true;
  },
  moveToPanel: (w) => {
    const ed = w.editorTerms();
    if (ed.length === 0) return false;
    const t = w.pick(ed);
    const at = w.tabOf(t);
    if (!at) return false;
    w.change(`moveToPanel ${t.id}`, () => { w.removeTab(at.g, at.tab); w.dropEmptyGroup(at.g); });
    // Spike A12: the moved terminal becomes active in the panel afterwards (387 ms there; anything here).
    w.tick(Math.floor(w.r() * 400));
    w.setActive(t, 'moved to panel');
    return true;
  },
  moveEditorToGroup: (w) => {
    const ed = w.editorTerms();
    if (ed.length === 0) return false;
    const t = w.pick(ed);
    const at = w.tabOf(t);
    if (!at) return false;
    const tab: Tab = { id: ++w.ids, term: t, label: at.tab.label };
    w.change(`move ${t.id} to group, tab ${tab.id}`, () => {
      let target = w.groups.length < 3 && w.r() < 0.5 ? w.groups.length : Math.floor(w.r() * w.groups.length);
      if (target === at.g) target = w.groups.length;
      if (target === w.groups.length) w.groups.push({ tabs: [], front: undefined });
      w.addTab(target, tab);
      w.removeTab(at.g, at.tab);
      w.activeGroup = target;
      const before = w.groups.length;
      w.dropEmptyGroup(at.g);
      if (w.groups.length < before && at.g < target) w.activeGroup = target - 1;
    });
    return true;
  },
  openFile: (w) => {
    const g = Math.floor(w.r() * w.groups.length);
    w.change(`open file in ${g}`, () => w.addTab(g, { id: ++w.ids, label: 'README.md' }));
    return true;
  },
  closeFile: (w) => {
    const g = w.groups.findIndex((x) => x.front && !x.front.term);
    if (g < 0) return false;
    w.change(`close file in ${g}`, () => { w.removeTab(g, w.groups[g].front as Tab); w.dropEmptyGroup(g); });
    return true;
  },
  focusGroup: (w) => {
    if (w.groups.length < 2) return false;
    w.change('focus group', () => { w.activeGroup = Math.floor(w.r() * w.groups.length); });
    return true;
  },
  focusPanel: (w) => {
    const panel = w.panelTerms();
    if (panel.length === 0) return false;
    w.setActive(w.pick(panel), 'focus panel');
    return true;
  },
  closeTerminal: (w) => {
    if (w.terms.length === 0) return false;
    const t = w.pick(w.terms);
    const at = w.tabOf(t);
    const wasActive = w.active === t;
    if (!at) {
      w.terms = w.terms.filter((x) => x !== t);
      w.emitTerms(`close terminal ${t.id}`);
      if (wasActive) w.setActive(w.terms[w.terms.length - 1], 'after close');
      return true;
    }
    // Closing the active terminal editor (third review of #2175, traced in the 1.147 bundle): the tab model
    // changes first (the tab behind it comes to the front, the tab closes), the terminal closes, then the
    // terminal editor host clears its active instance and VS Code falls back to the panel's last active
    // terminal, and only then does the tab now in front make its own terminal active.
    const before = w.activeFront();
    w.removeTab(at.g, at.tab);
    w.dropEmptyGroup(at.g);
    const after = w.activeFront();
    w.emitTabs(`its tab ${at.tab.id} closes`);
    w.terms = w.terms.filter((x) => x !== t);
    w.emitTerms(`close terminal ${t.id}`);
    if (wasActive) {
      const fallback = w.lastPanelActive && w.terms.includes(w.lastPanelActive) && !w.tabOf(w.lastPanelActive) ? w.lastPanelActive : undefined;
      if (fallback) w.setActive(fallback, 'panel fallback after closing the active editor terminal');
      else if (!after?.term) w.setActive(w.terms[w.terms.length - 1], 'after close');
    }
    if (after !== before && after?.term) w.setActive(after.term, `front after closing ${at.tab.id}`);
    return true;
  },
};

/** A restored window: terminals in the panel and in editor tabs, nothing learned. */
function restored(w: World): void {
  const n = Math.floor(w.r() * 4);
  for (let i = 0; i < n; i++) {
    const t = w.newTerm();
    w.terms.push(t);
    if (w.r() < 0.5) {
      if (w.groups.length < 2 && w.r() < 0.3) w.groups.push({ tabs: [], front: undefined });
      w.addTab(Math.floor(w.r() * w.groups.length), { id: ++w.ids, term: t, label: t.name });
    }
  }
  w.active = w.terms.length > 0 ? w.pick(w.terms) : undefined;
  w.loc.prime(w.layout());
  w.check('startup');
}

describe('TerminalLocator property: never in the wrong area', () => {
  // LOCATOR_PROPERTY_SEEDS=5000 npx vitest run test/unit/terminalLocator.property.test.ts for a deeper run.
  const seeds = Number(process.env.LOCATOR_PROPERTY_SEEDS) || 1500;
  it(`holds over ${seeds} random action sequences`, () => {
    const names = Object.keys(ACTIONS);
    for (let seed = 1; seed <= seeds; seed++) {
      const r = rng(seed);
      const w = new World(r);
      w.log.push(`seed ${seed}`);
      restored(w);
      for (let i = 0; i < 30; i++) {
        const name = names[Math.floor(r() * names.length)];
        if (ACTIONS[name](w)) w.tick(ACTION_GAP_MS + Math.floor(r() * 1200));
      }
    }
  }, Math.max(10_000, seeds * 20));
});
