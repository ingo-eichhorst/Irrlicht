// Types for address.js, for the hooks module (the engine's type check reads
// no JavaScript). Kept to what hooks/register.tsx imports; the behaviour is
// address.js's and is tested there (address.test.js).

export type DaemonAddress = {
  port: number
  source: 'env' | 'file' | 'default'
  addrFile: string | null
}

export type DaemonAddressIO = {
  bindAddr: () => Promise<string | undefined>
  irrlichtHome: () => Promise<string | undefined>
  home: () => Promise<string | undefined>
  stat: (path: string) => Promise<{ kind: string; isLink: boolean }>
  read: (path: string) => Promise<unknown>
}

export function resolveDaemonAddress(io: DaemonAddressIO): Promise<DaemonAddress>
export function hostPortOf(resolved: Pick<DaemonAddress, 'port'>): string
export function sessionsURL(resolved: Pick<DaemonAddress, 'port'>): string
