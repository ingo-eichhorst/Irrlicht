// Locates each terminal (panel, editor group, or unknown) by identity rather than by name. Pure: no vscode
// import; the controller passes vscode.Tab and vscode.Terminal objects in as opaque identities.
//
// Why it has to learn: VS Code gives an editor terminal tab no link to its Terminal (TabInputTerminal has no
// fields, and the Tab API object exposes no id), a user-created terminal's creationOptions carry no location
// (`{}` for every terminal created from the UI, spike #2175), and the tab label is the terminal's title, which
// agents share (Claude Code titles every terminal "✳ Claude Code"). A tab shows one terminal for its whole
// life (moving an editor to another group closes its tab and opens a new one), so a bound tab is never
// learned again. Three kinds of evidence bind a tab, strongest first:
//
// 1. Creation: a terminal tab and a terminal that both appear within PAIR_WINDOW_MS of each other, each the
//    only new one of its kind, are the same terminal (a terminal created in the editor area).
// 2. Transfer: a tab that closes while its terminal lives on hands that terminal to the one new tab with the
//    same label that appears within PAIR_WINDOW_MS (an editor moved to another group).
// 3. Activation, seen in the spike with VS Code 1.147: when a terminal tab becomes the active tab of the
//    active editor group, the terminal shown in it becomes vscode.window.activeTerminal a few milliseconds
//    later, or already is (then no event fires: the 1.147 extension host drops an unchanged active terminal
//    in $acceptActiveTerminalChanged). For a tab that has just opened only an active-terminal change inside
//    BIND_WINDOW_MS counts (moveToEditor fires one, spike A11); for a tab that already existed and came back
//    to the front, the terminal active when the window ends counts too (spike A8).
//
// Every binding needs the terminal's name to equal the tab's label once both are known (an empty title has
// not arrived yet); a candidate whose names never agree within CONFIRM_MAX_MS is dropped, leaving the
// terminals unknown rather than wrong. What activation cannot rule out: two terminals with the same name, a
// tab back in front with no learned terminal, and either the user switching to the other terminal within
// BIND_WINDOW_MS or the extension host delivering the active-terminal change later than that; the tab is
// then bound to the wrong one of the two.

/** How long after a terminal tab comes to the front an active-terminal change still belongs to that tab. */
export const BIND_WINDOW_MS = 300;
/** How far apart a new tab and a new (or released) terminal may appear and still be paired. */
export const PAIR_WINDOW_MS = 1000;
/** How long a candidate waits for the tab's label and the terminal's name to be known. */
export const CONFIRM_MAX_MS = 3000;

export interface GroupView<Tab> {
  /** The group's terminal tabs, each with its label. */
  readonly terminalTabs: ReadonlyArray<{ readonly tab: Tab; readonly label: string }>;
  /** The group's active tab when that is a terminal tab, otherwise undefined: what the group is showing. */
  readonly front: Tab | undefined;
  /** Whether this is the active editor group. */
  readonly active: boolean;
}

export interface Layout<Tab, Term> {
  /** Every editor group, in vscode.window.tabGroups.all order. */
  readonly groups: ReadonlyArray<GroupView<Tab>>;
  /** Every open terminal with its current name. */
  readonly terminals: ReadonlyArray<{ readonly terminal: Term; readonly name: string }>;
  readonly activeTerminal: Term | undefined;
}

/**
 * Where a terminal is. `front` is whether its tab is the one its group shows. `unknown`: an editor tab whose
 * terminal is not learned yet carries this terminal's name, so it may be in that tab or in the panel.
 */
export type Place = { area: 'panel' } | { area: 'editor'; group: number; front: boolean } | { area: 'unknown' };

export interface LocatorTimers {
  set(fn: () => void, ms: number): unknown;
  clear(handle: unknown): void;
}

/**
 * Called once a pending activation's window has passed, so the owner can locate() again even when no other
 * event arrives (a terminal tab back in front whose terminal was already active fires nothing).
 */
export interface SettleDue { timers: LocatorTimers; onDue: () => void }

interface Pending<Tab, Term> { tab: Tab; at: number; isNew: boolean; candidate?: Term }
interface Seen<T> { item: T; at: number }
interface Released<Term> { terminal: Term; label: string; at: number }

export class TerminalLocator<Tab, Term> {
  private readonly bound = new Map<Tab, Term>();
  /** Tabs whose learned terminal has closed while the tab is still listed: never a candidate for another terminal. */
  private readonly orphaned = new Set<Tab>();
  private readonly labels = new Map<Tab, string>();
  private readonly seenTerms = new Set<Term>();
  private newTabs: Array<Seen<Tab>> = [];
  private newTerms: Array<Seen<Term>> = [];
  private released: Array<Released<Term>> = [];
  private lastFront: Tab | undefined;
  private lastActive: Term | undefined;
  private pending: Pending<Tab, Term> | undefined;
  private dueTimer: unknown;

  constructor(
    private readonly now: () => number = Date.now,
    private readonly windowMs: number = BIND_WINDOW_MS,
    private readonly due?: SettleDue,
  ) {}

  /** What the locator holds, with `describe` naming tabs and terminals; only for the e2e's failure messages. */
  debugState(describe: (x: Tab | Term) => string): unknown {
    return {
      bound: [...this.bound].map(([tab, t]) => [describe(tab), describe(t)]),
      orphaned: this.orphaned.size,
      pending: this.pending && { tab: describe(this.pending.tab), ageMs: this.now() - this.pending.at, isNew: this.pending.isNew, candidate: this.pending.candidate && describe(this.pending.candidate) },
      newTabs: this.newTabs.length,
      newTerms: this.newTerms.length,
      released: this.released.length,
      lastFront: this.lastFront === undefined ? undefined : describe(this.lastFront),
      lastActive: this.lastActive === undefined ? undefined : describe(this.lastActive),
      dueArmed: this.dueTimer !== undefined,
    };
  }

  /** Cancels a pending settle callback. */
  dispose(): void {
    this.disarm();
  }

  /**
   * Records the layout at startup without learning from it: nothing in it is new, and a tab restored in front
   * says nothing about which terminal is active now (the panel may have had focus last).
   */
  prime(layout: Layout<Tab, Term>): void {
    this.track(layout, true);
    this.lastFront = activeFront(layout);
    this.lastActive = layout.activeTerminal;
  }

  /**
   * Call on every tab or tab-group change. When a terminal tab not bound yet has just come to the front of
   * the active group, an activation is pending and this returns true.
   */
  observeTabs(layout: Layout<Tab, Term>): boolean {
    this.track(layout, false);
    const front = activeFront(layout);
    if (front !== this.lastFront) {
      this.lastFront = front;
      this.pending = front === undefined || this.bound.has(front)
        ? undefined
        : { tab: front, at: this.now(), isNew: this.newTabs.some((s) => s.item === front) };
      if (this.pending === undefined) this.disarm();
      else this.arm();
    }
    return this.pending !== undefined;
  }

  /** Call on vscode.window.onDidChangeActiveTerminal, with the layout as it is now. */
  observeActiveTerminal(layout: Layout<Tab, Term>): void {
    const previous = this.lastActive;
    this.observeTabs(layout);
    this.lastActive = layout.activeTerminal;
    const p = this.pending;
    if (p === undefined || p.candidate !== undefined) return this.confirm(layout);
    if (this.now() - p.at <= this.windowMs) p.candidate = layout.activeTerminal;
    // Late, with no change inside the window: an existing tab's terminal was the one active until now.
    else if (!p.isNew) p.candidate = previous;
    else this.pending = undefined;
    this.confirm(layout);
  }

  /** Every open terminal's place. Settles a pending activation whose window has passed. */
  locate(layout: Layout<Tab, Term>): Map<Term, Place> {
    this.track(layout, false);
    this.settle(layout);
    this.confirm(layout);
    const tabTerm = this.resolveTabs(layout);
    const places = new Map<Term, Place>();
    layout.groups.forEach((g, group) => {
      for (const { tab } of g.terminalTabs) {
        const t = tabTerm.get(tab);
        if (t !== undefined) places.set(t, { area: 'editor', group, front: g.front === tab });
      }
    });
    const unresolved = new Set<string>();
    for (const g of layout.groups) {
      for (const { tab, label } of g.terminalTabs) if (!tabTerm.has(tab) && !this.orphaned.has(tab)) unresolved.add(label);
    }
    for (const { terminal, name } of layout.terminals) {
      if (!places.has(terminal)) places.set(terminal, unresolved.has(name) ? { area: 'unknown' } : { area: 'panel' });
    }
    return places;
  }

  private arm(): void {
    if (!this.due) return;
    this.disarm();
    const { timers, onDue } = this.due;
    this.dueTimer = timers.set(() => { this.dueTimer = undefined; onDue(); }, this.windowMs + 1);
  }

  private disarm(): void {
    if (this.dueTimer === undefined || !this.due) return;
    this.due.timers.clear(this.dueTimer);
    this.dueTimer = undefined;
  }

  /** Window passed with no active-terminal change: an existing tab takes the terminal active now; a new tab nothing. */
  private settle(layout: Layout<Tab, Term>): void {
    const p = this.pending;
    if (p === undefined || p.candidate !== undefined || this.now() - p.at <= this.windowMs) return;
    if (p.isNew || layout.activeTerminal === undefined) this.pending = undefined;
    else p.candidate = layout.activeTerminal;
  }

  /** Binds the pending tab to its candidate once both names are known and equal; drops it when they differ or time runs out. */
  private confirm(layout: Layout<Tab, Term>): void {
    const p = this.pending;
    if (p === undefined) return;
    if (this.bound.has(p.tab)) { this.pending = undefined; return; }
    const expired = this.now() - p.at > CONFIRM_MAX_MS;
    if (p.candidate === undefined) { if (expired) this.pending = undefined; return; }
    const label = this.labels.get(p.tab);
    const name = nameOf(layout, p.candidate);
    if (label === undefined || name === undefined) { this.pending = undefined; return; }
    if (label === '' || name === '') { if (expired) this.pending = undefined; return; }
    if (label === name) this.bind(p.tab, p.candidate);
    this.pending = undefined;
  }

  /** The learned bindings, plus any tab whose terminal follows by elimination: its label is carried by exactly one unclaimed terminal. */
  private resolveTabs(layout: Layout<Tab, Term>): Map<Tab, Term> {
    const tabTerm = new Map(this.bound);
    const claimed = new Set(this.bound.values());
    const byLabel = new Map<string, Tab[]>();
    for (const g of layout.groups) {
      for (const { tab, label } of g.terminalTabs) {
        if (tabTerm.has(tab) || this.orphaned.has(tab)) continue;
        byLabel.set(label, [...(byLabel.get(label) ?? []), tab]);
      }
    }
    for (const [label, tabs] of byLabel) {
      const candidates = layout.terminals.filter((t) => t.name === label && !claimed.has(t.terminal));
      if (tabs.length !== 1 || candidates.length !== 1) continue;
      tabTerm.set(tabs[0], candidates[0].terminal);
      claimed.add(candidates[0].terminal);
    }
    return tabTerm;
  }

  private bind(tab: Tab, terminal: Term): void {
    for (const [other, t] of this.bound) if (t === terminal) this.bound.delete(other);
    this.bound.set(tab, terminal);
    this.orphaned.delete(tab);
  }

  /** Records what is new, forgets what closed, and applies the creation and transfer evidence. */
  private track(layout: Layout<Tab, Term>, initial: boolean): void {
    const now = this.now();
    const tabs = new Map(layout.groups.flatMap((g) => g.terminalTabs.map((t) => [t.tab, t.label] as const)));
    const terminals = new Set(layout.terminals.map((t) => t.terminal));
    for (const [tab, label] of tabs) {
      if (!this.labels.has(tab) && !initial) this.newTabs.push({ item: tab, at: now });
      this.labels.set(tab, label);
    }
    for (const t of terminals) {
      if (!this.seenTerms.has(t) && !initial) this.newTerms.push({ item: t, at: now });
      this.seenTerms.add(t);
    }
    this.prune(tabs, terminals, now);
    const fresh = <T>(s: Seen<T> | Released<Term>): boolean => now - s.at <= PAIR_WINDOW_MS;
    this.newTabs = this.newTabs.filter((s) => fresh(s) && tabs.has(s.item) && !this.bound.has(s.item));
    this.newTerms = this.newTerms.filter((s) => fresh(s) && terminals.has(s.item));
    this.released = this.released.filter((r) => fresh(r) && terminals.has(r.terminal));
    this.pairTransfers(tabs);
    this.pairCreation(layout);
  }

  /** Transfer: a released terminal goes to the one new unbound tab with its label. */
  private pairTransfers(tabs: Map<Tab, string>): void {
    for (const r of [...this.released]) {
      if ([...this.bound.values()].includes(r.terminal)) continue;
      const matches = this.newTabs.filter((s) => !this.bound.has(s.item) && tabs.get(s.item) === r.label);
      if (matches.length !== 1) continue;
      this.bind(matches[0].item, r.terminal);
      this.released = this.released.filter((x) => x !== r);
    }
  }

  /** Creation: one new unbound tab and one new unbound terminal, appearing within PAIR_WINDOW_MS, names compatible. */
  private pairCreation(layout: Layout<Tab, Term>): void {
    const claimed = new Set(this.bound.values());
    const tabs = this.newTabs.filter((s) => !this.bound.has(s.item));
    const terms = this.newTerms.filter((s) => !claimed.has(s.item));
    if (tabs.length !== 1 || terms.length !== 1 || Math.abs(tabs[0].at - terms[0].at) > PAIR_WINDOW_MS) return;
    const label = this.labels.get(tabs[0].item) ?? '';
    const name = nameOf(layout, terms[0].item) ?? '';
    if (label !== '' && name !== '' && label !== name) return;
    this.bind(tabs[0].item, terms[0].item);
  }

  /**
   * Forgets closed tabs and closed terminals. A tab outliving its learned terminal becomes orphaned; a terminal
   * outliving its tab is released for a transfer.
   */
  private prune(tabs: Map<Tab, string>, terminals: Set<Term>, now: number): void {
    for (const [tab, t] of this.bound) {
      if (!tabs.has(tab)) {
        this.bound.delete(tab);
        if (terminals.has(t)) this.released.push({ terminal: t, label: this.labels.get(tab) ?? '', at: now });
      } else if (!terminals.has(t)) { this.bound.delete(tab); this.orphaned.add(tab); }
    }
    for (const tab of [...this.labels.keys()]) if (!tabs.has(tab)) this.labels.delete(tab);
    for (const tab of this.orphaned) if (!tabs.has(tab)) this.orphaned.delete(tab);
    for (const t of [...this.seenTerms]) if (!terminals.has(t)) this.seenTerms.delete(t);
    if (this.pending !== undefined && !tabs.has(this.pending.tab)) this.pending = undefined;
    if (this.lastActive !== undefined && !terminals.has(this.lastActive)) this.lastActive = undefined;
  }
}

function nameOf<Tab, Term>(layout: Layout<Tab, Term>, terminal: Term): string | undefined {
  return layout.terminals.find((t) => t.terminal === terminal)?.name;
}

/** The active editor group's front tab when it is a terminal tab. */
function activeFront<Tab, Term>(layout: Layout<Tab, Term>): Tab | undefined {
  return layout.groups.find((g) => g.active)?.front;
}
