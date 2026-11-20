// Types for format.js, for the hooks module (the engine's type check reads
// no JavaScript). Kept to what hooks/register.tsx imports; the behaviour is
// format.js's and is tested there (format.test.js).

import type { IrrlichtPanelRow } from '../types'

export function fitRow(row: Pick<IrrlichtPanelRow, 'branch' | 'tokens' | 'context' | 'model' | 'adapterTag' | 'isHere'>, width: number): string
export function quotaBar(percent: number, width: number): string
export function footerLines(dayCost: string, width: number): string[]
