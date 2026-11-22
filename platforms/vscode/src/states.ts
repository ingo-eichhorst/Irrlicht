// Canonical session-state vocabulary; mirrors core/domain/session canonicalStates.
export const STATES = ['working', 'waiting', 'ready', 'error'] as const;
export type StateName = typeof STATES[number];

export function isStateName(v: unknown): v is StateName {
  return typeof v === 'string' && (STATES as readonly string[]).includes(v);
}

export function stateColorId(s: StateName): string {
  return 'irrlicht.state.' + s;
}

// Codicon per state, matching the macOS app's SessionStateIcon.swift: a solid dot the size of the
// ready check, two pause bars, a filled circle with a check. Codicons have no filled
// circle-with-exclamation (the app's error glyph), so error uses VS Code's own circled error mark.
const STATE_ICONS: Record<StateName, string> = {
  working: 'circle-large-filled',
  waiting: 'debug-pause',
  ready: 'pass-filled',
  error: 'error',
};

/** The codicon id and theme colour id for a state. An unrecognised state gets an uncoloured question mark, never a state colour. */
export function stateIcon(state: unknown): { id: string; colorId?: string } {
  return isStateName(state) ? { id: STATE_ICONS[state], colorId: stateColorId(state) } : { id: 'question' };
}
