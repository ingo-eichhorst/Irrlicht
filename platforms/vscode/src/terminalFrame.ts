// Terminal frame: paints the state colour of the agent terminals each area shows around that area (the
// panel and the editor area, independently), by writing owned keys into the workspace's
// workbench.colorCustomizations (the approach Peacock uses for whole windows). Pure: no vscode import.
import type { StateName } from './states';

export type FrameMode = 'off' | 'border' | 'border-and-tint';
export const FRAME_MODES: readonly FrameMode[] = ['off', 'border', 'border-and-tint'];
export const DEFAULT_FRAME_MODE: FrameMode = 'border';

/** High-contrast themes map to the variant of the same brightness. */
export type ThemeKind = 'dark' | 'light';

/** Panel terminal: the 1.147 modern layout's card outline, plus the classic layout's rules. */
export const PANEL_BORDER_KEYS: readonly string[] = [
  'modernPanel.border', 'panel.border', 'panelTitle.activeBorder', 'terminal.tab.activeBorder', 'terminal.border',
];
/**
 * The modern layout's outline around the whole editor area. It spans every editor group (spike, VS Code
 * 1.140 and 1.147, #2175), so it is written only with one group, or where tab edges are not drawn.
 */
export const EDITOR_BOX_KEY = 'editor.border';
/** The top edge of the active tab in the active editor group: paints that one tab only (spike, #2175). */
export const TAB_ACTIVE_KEY = 'tab.activeBorderTop';
/** The top edge of the active tab in every other editor group: paints each of those groups' front tab. */
export const TAB_UNFOCUSED_KEY = 'tab.unfocusedActiveBorderTop';
/** Every editor-area key the frame may write. */
export const EDITOR_BORDER_KEYS: readonly string[] = [EDITOR_BOX_KEY, TAB_ACTIVE_KEY, TAB_UNFOCUSED_KEY];
/**
 * Written by the frame before #2175 and never written now (it paints every separator between editor
 * groups). Still owned, so a value an earlier build left on disk is restored rather than kept forever.
 */
export const RETIRED_KEYS: readonly string[] = ['editorGroup.border'];
/** Written only in border-and-tint mode; panel.background only while the panel is framed. */
export const TINT_KEYS: readonly string[] = ['terminal.background', 'panel.background'];
/** Every key this extension may write or once wrote. Any other key in workbench.colorCustomizations is the user's. */
export const OWNED_KEYS: readonly string[] = [...PANEL_BORDER_KEYS, ...EDITOR_BORDER_KEYS, ...RETIRED_KEYS, ...TINT_KEYS];

/**
 * The canonical states (states.ts STATES), most urgent first: several visible agent terminals in one area
 * show the first of these. The unit tests check it orders exactly STATES.
 */
export const URGENCY: readonly StateName[] = ['error', 'waiting', 'working', 'ready'];

/** The most urgent canonical state among `states`; anything else is ignored. undefined when none is canonical. */
export function mostUrgent(states: readonly unknown[]): StateName | undefined {
  return URGENCY.find((u) => states.includes(u));
}

/** The first VS Code release whose modern layout draws tab.activeBorderTop / tab.unfocusedActiveBorderTop. */
export const TAB_EDGES_SINCE: readonly [number, number] = [1, 147];

/**
 * Whether the editor tab edges (TAB_ACTIVE_KEY, TAB_UNFOCUSED_KEY) are drawn. The classic layout draws
 * them; the modern layout (`workbench.experimental.modernUI`) draws them from 1.147 on. In 1.140 the
 * connected tab style overrides them with its own border (its CSS sets
 * --modern-ui-editor-tab-active-border-top to the connected-tab border and has no
 * --modern-ui-editor-tab-custom-active-border-top; 1.147 has three uses of it), and a screenshot spike saw
 * no tab edge in 1.140 and both in 1.147 (#2175). 1.141 to 1.146 were not tested and get the box.
 * `modernUI` is the setting's value: undefined where the setting does not exist (the classic layout only).
 */
export function tabEdgesDrawn(version: string, modernUI: unknown): boolean {
  if (modernUI !== true) return true;
  const m = /^(\d+)\.(\d+)/.exec(version);
  if (!m) return false;
  const [major, minor] = [Number(m[1]), Number(m[2])];
  return major > TAB_EDGES_SINCE[0] || (major === TAB_EDGES_SINCE[0] && minor >= TAB_EDGES_SINCE[1]);
}

/** The irrlicht.state.* defaults from package.json contributes.colors (dark = dark/highContrast, light = light/highContrastLight). */
export const STATE_COLORS: Readonly<Record<StateName, Readonly<Record<ThemeKind, string>>>> = {
  working: { dark: '#8B5CF6', light: '#6526F3' },
  waiting: { dark: '#FF9500', light: '#884F00' },
  ready: { dark: '#34C759', light: '#207936' },
  error: { dark: '#FF3B30', light: '#CC0C00' },
};

/**
 * The background the tint is blended over. The extension API cannot read the active theme's
 * real colours, so this assumes Dark Modern / Light Modern's panel background (VS Code 1.147's default
 * 2026 themes use #191A1B / #FAFAFD, a few shades off). The blend must be
 * opaque: an alpha terminal.background breaks xterm's cell rendering (spike, VS Code 1.147).
 */
export const TINT_BASE: Readonly<Record<ThemeKind, string>> = { dark: '#181818', light: '#F8F8F8' };
export const TINT_AMOUNT: Readonly<Record<ThemeKind, number>> = { dark: 0.14, light: 0.1 };

export const FRAME_DEBOUNCE_MS = 200;

type Obj = Record<string, unknown>;

function isPlainObject(v: unknown): v is Obj {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function has(o: Obj, key: string): boolean {
  return Object.hasOwn(o, key);
}

/** Structural equality for JSON-like values; object key order does not matter. */
function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (Array.isArray(a) || Array.isArray(b)) return arraysEqual(a, b);
  if (!isPlainObject(a) || !isPlainObject(b)) return false;
  return objectsEqual(a, b);
}

function arraysEqual(a: unknown, b: unknown): boolean {
  if (!Array.isArray(a) || !Array.isArray(b)) return false;
  return a.length === b.length && a.every((v, i) => deepEqual(v, b[i]));
}

function objectsEqual(a: Obj, b: Obj): boolean {
  const keysA = Object.keys(a);
  return keysA.length === Object.keys(b).length && keysA.every((k) => has(b, k) && deepEqual(a[k], b[k]));
}

/** Unknown or missing values give DEFAULT_FRAME_MODE. */
export function parseFrameMode(raw: unknown): FrameMode {
  return FRAME_MODES.find((m) => m === raw) ?? DEFAULT_FRAME_MODE;
}

/** vscode.ColorThemeKind: 1 Light, 2 Dark, 3 HighContrast, 4 HighContrastLight. */
export function themeKindOf(vscodeKind: number): ThemeKind {
  return vscodeKind === 1 || vscodeKind === 4 ? 'light' : 'dark';
}

const HEX_COLOR = /^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/;

/** '#RGB' or '#RRGGBB' (any case) to uppercase '#RRGGBB'; undefined for anything else. */
function normalizeHex(value: unknown): string | undefined {
  if (typeof value !== 'string' || !HEX_COLOR.test(value)) return undefined;
  const digits = value.slice(1).toUpperCase();
  const full = digits.length === 3 ? digits.split('').map((c) => c + c).join('') : digits;
  return '#' + full;
}

/**
 * The state colour as '#RRGGBB'. A top-level `irrlicht.state.<state>` entry in the effective
 * workbench.colorCustomizations wins when it is a #RGB or #RRGGBB hex value.
 */
export function resolveStateColor(state: StateName, kind: ThemeKind, customizations?: unknown): string {
  if (isPlainObject(customizations)) {
    const override = normalizeHex(customizations['irrlicht.state.' + state]);
    if (override) return override;
  }
  return STATE_COLORS[state][kind];
}

function channels(hex: string): number[] {
  return [1, 3, 5].map((i) => Number.parseInt(hex.slice(i, i + 2), 16));
}

/** Opaque mix of `over` into `base` (both #RRGGBB); amount 0 = base, 1 = over. Returns uppercase '#RRGGBB'. */
export function blend(base: string, over: string, amount: number): string {
  const from = channels(base);
  const to = channels(over);
  const mixed = from.map((b, i) => Math.round(b + (to[i] - b) * amount));
  return '#' + mixed.map((c) => c.toString(16).padStart(2, '0')).join('').toUpperCase();
}

function fillKeys(keys: readonly string[], value: string): Record<string, string> {
  return Object.fromEntries(keys.map((k) => [k, value]));
}

/** What the editor area shows. Each state is a paired session's state of an agent terminal in front of its group. */
export interface EditorView {
  /** The agent terminal in front of the active editor group (zero or one state). */
  focused: readonly unknown[];
  /** The agent terminals in front of every other editor group. */
  unfocused: readonly unknown[];
  /** How many editor groups there are (at least one). */
  groupCount: number;
  /** tabEdgesDrawn(...) for this VS Code. */
  tabEdges: boolean;
}

export interface FrameInput {
  mode: FrameMode;
  kind: ThemeKind;
  /**
   * The paired sessions' states of the agent terminals in the panel. VS Code does not tell an extension
   * which panel terminals are on screen (split panes) and which hide behind another panel tab, so this is
   * every agent terminal in the panel.
   */
  panel: readonly unknown[];
  editor: EditorView;
  /** The effective workbench.colorCustomizations, read only for irrlicht.state.* overrides. */
  customizations?: unknown;
}

/**
 * The editor-area keys. One group: the box and the active-tab edge (the box is exactly that group). Two or
 * more groups: the tab edges only, each carrying its own bucket's most urgent state, because the box spans
 * every group; where the tab edges are not drawn, the box anyway, in the most urgent editor state.
 */
function editorColors(view: EditorView, color: (s: StateName) => string): Record<string, string> {
  const any = mostUrgent([...view.focused, ...view.unfocused]);
  if (any === undefined) return {};
  const keys: Record<string, string> = {};
  if (view.groupCount <= 1 || !view.tabEdges) keys[EDITOR_BOX_KEY] = color(any);
  const focused = mostUrgent(view.focused);
  if (focused !== undefined) keys[TAB_ACTIVE_KEY] = color(focused);
  const unfocused = mostUrgent(view.unfocused);
  if (unfocused !== undefined) keys[TAB_UNFOCUSED_KEY] = color(unfocused);
  return keys;
}

/**
 * The owned keys to write. Each area is framed by its own agent terminals: the panel by the most urgent
 * state among them, the editor area per editorColors. 'off', or no canonical state anywhere, gives {}.
 * border-and-tint adds one tint for the window (terminal.background is a single key for every terminal)
 * from the most urgent framed state, and the same tint on panel.background while the panel is framed.
 */
export function desiredFrame(input: FrameInput): Record<string, string> {
  if (input.mode === 'off') return {};
  const color = (s: StateName): string => resolveStateColor(s, input.kind, input.customizations);
  const panel = mostUrgent(input.panel);
  const out: Record<string, string> = panel === undefined ? {} : fillKeys(PANEL_BORDER_KEYS, color(panel));
  Object.assign(out, editorColors(input.editor, color));
  const worst = mostUrgent([...input.panel, ...input.editor.focused, ...input.editor.unfocused]);
  if (input.mode === 'border-and-tint' && worst !== undefined) {
    const tint = blend(TINT_BASE[input.kind], color(worst), TINT_AMOUNT[input.kind]);
    out['terminal.background'] = tint;
    if (panel !== undefined) out['panel.background'] = tint;
  }
  return out;
}

/**
 * Persisted in workspaceState while a frame is applied. It must describe what is on disk, not
 * what we intend: a write can be refused or the process can die before it lands, and a memo
 * that claimed colours the disk does not hold would make the old frame look like the user's own.
 */
export interface FrameMemo {
  /** The user's own value for each owned key; an absent key means the user had none. */
  prev: Record<string, unknown>;
  /** The colours a successful write has confirmed on disk, per owned key. */
  written: Record<string, string>;
  /**
   * The colours of a write that started but is not confirmed (it may or may not have landed).
   * Present only between the pre-write save and the post-write save.
   */
  pending?: Record<string, string>;
  /** Whether the workspace had a workbench.colorCustomizations object before the first write. */
  hadObject: boolean;
}

function isStringRecord(v: unknown): v is Record<string, string> {
  return isPlainObject(v) && Object.values(v).every((x) => typeof x === 'string');
}

/**
 * Reads a memo from persisted storage, which may hold anything: an older shape, a partial write,
 * junk. Returns a valid memo, or undefined when it cannot be read with confidence. An earlier,
 * unreleased commit of this feature (fa19cd64a) stored `applied` where `written` is now; it is migrated. A malformed `pending` is
 * dropped, since losing it only loses the "may have landed" hint.
 */
export function normalizeMemo(raw: unknown): FrameMemo | undefined {
  if (!isPlainObject(raw)) return undefined;
  const written = has(raw, 'written') ? raw.written : raw.applied;
  if (!isPlainObject(raw.prev) || !isStringRecord(written)) return undefined;
  if (typeof raw.hadObject !== 'boolean') return undefined;
  const memo: FrameMemo = { prev: raw.prev, written, hadObject: raw.hadObject };
  if (isStringRecord(raw.pending)) memo.pending = raw.pending;
  return memo;
}

export interface FramePlan {
  /** The workspace-scope value to write; undefined removes the setting. */
  next: unknown;
  /** True when next differs from current (key order ignored). */
  changed: boolean;
  /** The memo to persist; undefined once nothing is applied. */
  memo: FrameMemo | undefined;
  /** current is defined but not a plain object: nothing is written. */
  invalid: boolean;
}

function ownedOnly(desired: Record<string, string>): Record<string, string> {
  const kept: Record<string, string> = {};
  for (const k of OWNED_KEYS) {
    if (has(desired, k)) kept[k] = desired[k];
  }
  return kept;
}

/** True when `value` is exactly a colour this extension wrote, or may have written, for `key`. */
function isOurs(memo: FrameMemo, key: string, value: unknown): boolean {
  if (has(memo.written, key) && deepEqual(value, memo.written[key])) return true;
  return memo.pending !== undefined && has(memo.pending, key) && deepEqual(value, memo.pending[key]);
}

/** The owned keys whose value on disk is known to be ours, with that value. */
function confirmedOnDisk(cur: Obj, memo: FrameMemo | undefined): Record<string, string> {
  const confirmed: Record<string, string> = {};
  if (memo === undefined) return confirmed;
  for (const k of OWNED_KEYS) {
    if (has(cur, k) && isOurs(memo, k, cur[k])) confirmed[k] = cur[k] as string;
  }
  return confirmed;
}

/**
 * The user's own value for each owned key they have one for. A key still holding exactly what
 * we wrote is ours, so the memo's snapshot stands in for it; any other value (a user edit made
 * while a frame was applied, or a key we never wrote) is the user's latest.
 */
function userValuesOf(cur: Obj, memo: FrameMemo | undefined): Obj {
  const values: Obj = {};
  for (const k of OWNED_KEYS) {
    const stillOurs = memo !== undefined && isOurs(memo, k, cur[k]);
    const source = stillOurs ? memo.prev : cur;
    if (has(source, k)) values[k] = source[k];
  }
  return values;
}

/** Desired colour where there is one, else the user's value, else the key is gone. */
function mergeOwned(cur: Obj, desired: Record<string, string>, userValues: Obj): Obj {
  const next: Obj = { ...cur };
  for (const k of OWNED_KEYS) {
    if (has(desired, k)) next[k] = desired[k];
    else if (has(userValues, k)) next[k] = userValues[k];
    else delete next[k];
  }
  return next;
}

/**
 * Merges `desired` into `current` (the WORKSPACE-scope value, never the effective one).
 * The returned memo is the one to persist AFTER the write succeeded (no `pending`).
 * See the restore rule in the module's tests.
 */
export function planFrame(current: unknown, memo: FrameMemo | undefined, desiredIn: Record<string, string>): FramePlan {
  if (!isPlainOrMissing(current)) {
    return { invalid: true, changed: false, next: current, memo };
  }
  const desired = ownedOnly(desiredIn);
  const restoring = Object.keys(desired).length === 0;
  if (memo === undefined && restoring) return { invalid: false, changed: false, next: current, memo: undefined };

  const cur: Obj = current ?? {};
  const userValues = userValuesOf(cur, memo);
  const hadObject = hadObjectBefore(current, memo);
  const next = mergedValue(mergeOwned(cur, desired, userValues), restoring && !hadObject);

  const planMemo = restoring ? undefined : { prev: userValues, written: desired, hadObject };
  return { invalid: false, changed: !deepEqual(next, current), next, memo: planMemo };
}

function isPlainOrMissing(v: unknown): v is Obj | undefined {
  return v === undefined || isPlainObject(v);
}

/** Whether the workspace had a colorCustomizations object before our first write. */
function hadObjectBefore(current: Obj | undefined, memo: FrameMemo | undefined): boolean {
  return memo ? memo.hadObject : current !== undefined;
}

/** The merged value, or undefined (remove the setting) when it is empty and `removable`. */
function mergedValue(merged: Obj, removable: boolean): Obj | undefined {
  return removable && Object.keys(merged).length === 0 ? undefined : merged;
}

export interface FrameIO {
  /** inspect('workbench.colorCustomizations').workspaceValue */
  read(): unknown;
  /** update(..., ConfigurationTarget.Workspace) */
  write(next: unknown): Promise<void>;
  loadMemo(): FrameMemo | undefined;
  saveMemo(memo: FrameMemo | undefined): Promise<void>;
  log(message: string): void;
}

export interface FrameTimers {
  set(fn: () => void, ms: number): unknown;
  clear(handle: unknown): void;
}

const REAL_TIMERS: FrameTimers = {
  set: (fn, ms) => setTimeout(fn, ms),
  clear: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

const INVALID_MESSAGE = 'workspace workbench.colorCustomizations is not an object; terminal frame left alone';

/** Debounced, serialized writer: one write in flight, the latest request wins. */
export class FrameSync {
  private latest: Record<string, string> | undefined;
  private timer: unknown;
  private timerArmed = false;
  private dirty = false;
  /** The request a pass was last asked to apply (by settle or the debounce timer). */
  private queued: Record<string, string> | undefined;
  private running = false;
  private loop: Promise<void> | undefined;
  private disposed = false;
  private readonly logged = new Set<string>();

  constructor(
    private readonly io: FrameIO,
    private readonly timers: FrameTimers = REAL_TIMERS,
    private readonly debounceMs: number = FRAME_DEBOUNCE_MS,
  ) {}

  /** Records `desired` as the latest request and (re)arms the debounce timer. Ignored after dispose. */
  request(desired: Record<string, string>): void {
    if (this.disposed) return;
    this.latest = desired;
    this.cancelTimer();
    this.timer = this.timers.set(() => {
      this.timerArmed = false;
      // pass() catches every error, so this promise cannot reject.
      void this.markDirtyAndPump();
    }, this.debounceMs);
    this.timerArmed = true;
  }

  /** Cancels a pending timer, applies the latest request now, and resolves once nothing is in flight or pending. */
  settle(): Promise<void> {
    this.cancelTimer();
    return this.markDirtyAndPump();
  }

  /** request({}) then settle(): puts the user's values back and clears the memo. */
  restore(): Promise<void> {
    this.request({});
    return this.settle();
  }

  /**
   * Cancels a pending timer and drops the request it was waiting for; later requests are ignored.
   * A pass already asked for through settle() or restore() is NOT discarded: VS Code disposes
   * subscriptions right after deactivate() without awaiting it, and a restore that can still land must.
   * Does not restore by itself.
   */
  dispose(): void {
    this.disposed = true;
    if (this.timerArmed) this.latest = this.queued;
    this.cancelTimer();
  }

  private cancelTimer(): void {
    if (!this.timerArmed) return;
    this.timers.clear(this.timer);
    this.timerArmed = false;
  }

  private markDirtyAndPump(): Promise<void> {
    if (this.latest !== undefined) {
      this.queued = this.latest;
      this.dirty = true;
    }
    return this.pump();
  }

  /**
   * Starts the single pass loop if none is running; every caller waits for the same loop.
   * `running` is cleared inside runPasses in the same synchronous segment as its last dirty
   * check, so a request landing after that check always starts a new loop instead of
   * joining one that is already finishing.
   */
  private pump(): Promise<void> {
    if (this.running && this.loop) return this.loop;
    this.running = true;
    const loop = this.runPasses();
    this.loop = loop;
    return loop;
  }

  private async runPasses(): Promise<void> {
    try {
      while (this.dirty && this.queued !== undefined) {
        this.dirty = false;
        // Awaiting inside the loop is deliberate: passes must run one at a time.
        await this.pass(this.queued);
      }
    } finally {
      this.running = false;
    }
  }

  /**
   * Memo and write ordering. Before a write that may change the disk, the memo is saved with, as
   * `written`, the owned values on disk that are known to be ours, and the incoming colours as
   * `pending`. A refused or interrupted write therefore leaves a memo that still recognises
   * whichever frame is on disk, even one that landed without being confirmed. The after-success
   * memo is saved only once the write resolved. A refused restoring write keeps the stored memo,
   * which still describes the disk. `stored` is whether anything, readable or not, sits in storage.
   */
  private async apply(plan: FramePlan, current: unknown, loaded: FrameMemo | undefined, stored: boolean): Promise<void> {
    if (plan.memo === undefined) return this.applyRestore(plan, stored);
    if (!plan.changed) return this.refreshMemo(plan.memo, loaded);
    const written = confirmedOnDisk(isPlainObject(current) ? current : {}, loaded);
    await this.io.saveMemo({ ...plan.memo, written, pending: plan.memo.written });
    await this.io.write(plan.next);
    await this.io.saveMemo(plan.memo);
  }

  /** A restoring plan: write the user's values back if they differ, then drop the stored memo. */
  private async applyRestore(plan: FramePlan, stored: boolean): Promise<void> {
    if (plan.changed) await this.io.write(plan.next);
    if (stored) await this.io.saveMemo(undefined);
  }

  /** Nothing to write: only bring the stored memo up to date. */
  private async refreshMemo(memo: FrameMemo, loaded: FrameMemo | undefined): Promise<void> {
    if (!deepEqual(memo, loaded)) await this.io.saveMemo(memo);
  }

  private async pass(desired: Record<string, string>): Promise<void> {
    try {
      const stored: unknown = this.io.loadMemo();
      const memo = normalizeMemo(stored);
      if (stored !== undefined && memo === undefined) this.logOnce('terminal frame: ignoring an unreadable memo');
      const current = this.io.read();
      const plan = planFrame(current, memo, desired);
      if (plan.invalid) {
        this.logOnce(INVALID_MESSAGE);
        return;
      }
      await this.apply(plan, current, memo, stored !== undefined);
    } catch (error) {
      this.logOnce('terminal frame: ' + (error instanceof Error ? error.message : String(error)));
    }
  }

  private logOnce(message: string): void {
    if (this.logged.has(message)) return;
    this.logged.add(message);
    this.io.log(message);
  }
}
