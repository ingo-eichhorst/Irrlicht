// Terminal frame: paints the state colour of the session in the active terminal around the
// terminal area, by writing owned keys into the workspace's workbench.colorCustomizations
// (the approach Peacock uses for whole windows). Pure: no vscode import.
import { isStateName } from './states';
import type { StateName } from './states';

export type FrameMode = 'off' | 'border' | 'border-and-tint';
export const FRAME_MODES: readonly FrameMode[] = ['off', 'border', 'border-and-tint'];
export const DEFAULT_FRAME_MODE: FrameMode = 'border';

/** Where the active terminal is shown: the panel, or a visible editor tab. */
export type FrameLocation = 'panel' | 'editor';
/** High-contrast themes map to the variant of the same brightness. */
export type ThemeKind = 'dark' | 'light';

/** Panel terminal: the 1.147 modern layout's card outline, plus the classic layout's rules. */
export const PANEL_BORDER_KEYS: readonly string[] = [
  'modernPanel.border', 'panel.border', 'panelTitle.activeBorder', 'terminal.tab.activeBorder', 'terminal.border',
];
/** Editor-area terminal: the modern layout's editor card outline, plus the classic layout's tab and group rules. */
export const EDITOR_BORDER_KEYS: readonly string[] = [
  'editor.border', 'tab.activeBorderTop', 'tab.unfocusedActiveBorderTop', 'editorGroup.border',
];
/** Written only in border-and-tint mode; panel.background only for a panel terminal. */
export const TINT_KEYS: readonly string[] = ['terminal.background', 'panel.background'];
/** Every key this extension may write. Any other key in workbench.colorCustomizations is the user's. */
export const OWNED_KEYS: readonly string[] = [...PANEL_BORDER_KEYS, ...EDITOR_BORDER_KEYS, ...TINT_KEYS];

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
  return Object.prototype.hasOwnProperty.call(o, key);
}

/** Structural equality for JSON-like values; object key order does not matter. */
function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false;
    return a.every((v, i) => deepEqual(v, b[i]));
  }
  if (!isPlainObject(a) || !isPlainObject(b)) return false;
  const keysA = Object.keys(a);
  if (keysA.length !== Object.keys(b).length) return false;
  return keysA.every((k) => has(b, k) && deepEqual(a[k], b[k]));
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
  return [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16));
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

/** The owned keys to write for one colour. 'off' gives {}. */
export function frameColors(color: string, kind: ThemeKind, location: FrameLocation, mode: FrameMode): Record<string, string> {
  if (mode === 'off') return {};
  const colors = fillKeys(location === 'panel' ? PANEL_BORDER_KEYS : EDITOR_BORDER_KEYS, color);
  if (mode === 'border-and-tint') {
    const tint = blend(TINT_BASE[kind], color, TINT_AMOUNT[kind]);
    colors['terminal.background'] = tint;
    if (location === 'panel') colors['panel.background'] = tint;
  }
  return colors;
}

export interface FrameInput {
  mode: FrameMode;
  /** The paired session's state, or undefined when the active terminal has no paired session. */
  state: unknown;
  kind: ThemeKind;
  location: FrameLocation;
  /** The effective workbench.colorCustomizations, read only for irrlicht.state.* overrides. */
  customizations?: unknown;
}

/** {} when the mode is off or the state is not a canonical state; otherwise frameColors(...). */
export function desiredFrame(input: FrameInput): Record<string, string> {
  if (input.mode === 'off' || !isStateName(input.state)) return {};
  const color = resolveStateColor(input.state, input.kind, input.customizations);
  return frameColors(color, input.kind, input.location, input.mode);
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
  if (current !== undefined && !isPlainObject(current)) {
    return { invalid: true, changed: false, next: current, memo };
  }
  const desired = ownedOnly(desiredIn);
  const restoring = Object.keys(desired).length === 0;
  if (memo === undefined && restoring) return { invalid: false, changed: false, next: current, memo: undefined };

  const cur: Obj = current ?? {};
  const userValues = userValuesOf(cur, memo);
  let next: unknown = mergeOwned(cur, desired, userValues);
  const hadObject = memo ? memo.hadObject : current !== undefined;
  if (restoring && Object.keys(next as Obj).length === 0 && !hadObject) next = undefined;

  const planMemo = restoring ? undefined : { prev: userValues, written: desired, hadObject };
  return { invalid: false, changed: !deepEqual(next, current), next, memo: planMemo };
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
      this.markDirtyAndPump();
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
        await this.pass(this.queued);
      }
    } finally {
      this.running = false;
    }
  }

  /**
   * Memo and write ordering. Before a write that may change the disk, the memo is saved with the
   * colours currently confirmed as `written` and the incoming ones as `pending`, so a refused or
   * interrupted write leaves a memo that still recognises whichever frame is on disk. The
   * after-success memo is saved only once the write resolved. A refused restoring write keeps the
   * loaded memo, which still describes the disk.
   */
  private async apply(plan: FramePlan, loaded: FrameMemo | undefined): Promise<void> {
    if (plan.memo === undefined) {
      if (plan.changed) await this.io.write(plan.next);
      if (loaded !== undefined) await this.io.saveMemo(undefined);
      return;
    }
    if (!plan.changed) {
      if (!deepEqual(plan.memo, loaded)) await this.io.saveMemo(plan.memo);
      return;
    }
    await this.io.saveMemo({ ...plan.memo, written: loaded?.written ?? {}, pending: plan.memo.written });
    await this.io.write(plan.next);
    await this.io.saveMemo(plan.memo);
  }

  private async pass(desired: Record<string, string>): Promise<void> {
    try {
      const memo = this.io.loadMemo();
      const plan = planFrame(this.io.read(), memo, desired);
      if (plan.invalid) {
        this.logOnce(INVALID_MESSAGE);
        return;
      }
      await this.apply(plan, memo);
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
