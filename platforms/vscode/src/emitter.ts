// Tiny pure event emitter shared by the client and the store. No vscode import.
export type Listener<T> = (v: T) => void;

export class Emitter<T> {
  private readonly listeners = new Set<Listener<T>>();

  /** `onError` receives whatever a listener throws; a listener never breaks the emitter or its siblings. */
  constructor(private readonly onError?: (e: unknown) => void) {}

  readonly event = (l: Listener<T>): { dispose(): void } => {
    this.listeners.add(l);
    return { dispose: () => { this.listeners.delete(l); } };
  };

  fire(v: T): void {
    // A snapshot: a listener added or removed while firing takes effect from the next fire (emitter.test.ts).
    const snapshot = [...this.listeners];
    for (const l of snapshot) {
      try { l(v); } catch (e) { this.onError?.(e); }
    }
  }
}
