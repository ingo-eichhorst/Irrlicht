// Locates each terminal (panel, editor group, or unknown) by identity rather than by name. Pure: no vscode
// import; the controller passes vscode.Tab and vscode.Terminal objects in as opaque identities.
//
// Why it has to learn: VS Code gives an editor terminal tab no link to its Terminal (TabInputTerminal has no
// fields, and the Tab API object exposes no id), a user-created terminal's creationOptions carry no location
// (`{}` for every terminal created from the UI, spike #2175), and the tab label is the terminal's title, which
// agents share (Claude Code titles every terminal "✳ Claude Code"). What VS Code does do, seen in the spike
// with 1.147: when a terminal tab becomes the active tab of the active editor group, the terminal shown in
// it becomes vscode.window.activeTerminal a few milliseconds later (or already is, then no event fires).
// That pairing is the binding this module learns.

/** How long after a terminal tab comes to the front an active-terminal change still belongs to that tab. */
export const BIND_WINDOW_MS = 300;

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

interface Pending<Tab> { tab: Tab; at: number }

export class TerminalLocator<Tab, Term> {
  private readonly bound = new Map<Tab, Term>();
  /** Tabs whose learned terminal has closed while the tab is still listed: never a candidate for another terminal. */
  private readonly orphaned = new Set<Tab>();
  private lastFront: Tab | undefined;
  private lastActive: Term | undefined;
  private pending: Pending<Tab> | undefined;

  constructor(private readonly now: () => number = Date.now, private readonly windowMs: number = BIND_WINDOW_MS) {}

  /**
   * Records the layout at startup without learning from it: a tab restored in front says nothing about which
   * terminal is active now (the panel may have had focus last).
   */
  prime(layout: Layout<Tab, Term>): void {
    this.prune(layout);
    this.lastFront = activeFront(layout);
    this.lastActive = layout.activeTerminal;
  }

  /**
   * Call on every tab or tab-group change. When a terminal tab has just come to the front of the active
   * group, a binding is pending and this returns true: call locate() again once windowMs has passed.
   */
  observeTabs(layout: Layout<Tab, Term>): boolean {
    this.prune(layout);
    const front = activeFront(layout);
    if (front !== this.lastFront) {
      this.lastFront = front;
      this.pending = front === undefined ? undefined : { tab: front, at: this.now() };
    }
    return this.pending !== undefined;
  }

  /** Call on vscode.window.onDidChangeActiveTerminal, with the layout as it is now. */
  observeActiveTerminal(layout: Layout<Tab, Term>): void {
    const previous = this.lastActive;
    this.observeTabs(layout);
    this.lastActive = layout.activeTerminal;
    const p = this.pending;
    if (p === undefined) return;
    this.pending = undefined;
    // Late: no change came inside the window, so the terminal active until now was already the tab's.
    const t = this.now() - p.at > this.windowMs ? previous : layout.activeTerminal;
    if (t !== undefined) this.bind(p.tab, t);
  }

  /** Every open terminal's place. Settles a pending binding whose window has passed. */
  locate(layout: Layout<Tab, Term>): Map<Term, Place> {
    this.prune(layout);
    this.settle(layout);
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

  private settle(layout: Layout<Tab, Term>): void {
    const p = this.pending;
    if (p === undefined || this.now() - p.at <= this.windowMs) return;
    this.pending = undefined;
    if (layout.activeTerminal !== undefined) this.bind(p.tab, layout.activeTerminal);
  }

  private bind(tab: Tab, terminal: Term): void {
    for (const [other, t] of this.bound) if (t === terminal) this.bound.delete(other);
    this.bound.set(tab, terminal);
    this.orphaned.delete(tab);
  }

  /** Forgets closed tabs and closed terminals; a tab outliving its learned terminal becomes orphaned. */
  private prune(layout: Layout<Tab, Term>): void {
    const tabs = new Set(layout.groups.flatMap((g) => g.terminalTabs.map((t) => t.tab)));
    const terminals = new Set(layout.terminals.map((t) => t.terminal));
    for (const [tab, t] of this.bound) {
      if (!tabs.has(tab)) this.bound.delete(tab);
      else if (!terminals.has(t)) { this.bound.delete(tab); this.orphaned.add(tab); }
    }
    for (const tab of this.orphaned) if (!tabs.has(tab)) this.orphaned.delete(tab);
    if (this.pending !== undefined && !tabs.has(this.pending.tab)) this.pending = undefined;
    if (this.lastActive !== undefined && !terminals.has(this.lastActive)) this.lastActive = undefined;
  }
}

/** The active editor group's front tab when it is a terminal tab. */
function activeFront<Tab, Term>(layout: Layout<Tab, Term>): Tab | undefined {
  return layout.groups.find((g) => g.active)?.front;
}
