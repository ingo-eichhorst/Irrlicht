// Canonical session-state vocabulary; mirrors core/domain/session canonicalStates.
export const STATES = ['working', 'waiting', 'ready', 'error'] as const;
export type StateName = typeof STATES[number];

export function isStateName(v: unknown): v is StateName {
  return typeof v === 'string' && (STATES as readonly string[]).includes(v);
}

export function stateColorId(s: StateName): string {
  return 'irrlicht.state.' + s;
}
