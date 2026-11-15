// Types for view.js, for the hooks module (the engine's type check reads no
// JavaScript). Kept to what hooks/register.tsx imports; the behaviour is
// view.js's and is tested there (view.test.js).

import type { HttpResponse } from 'claude-code'
import type { IrrlichtPanelView } from '../types'

export function unreachableView(hostPort: string, reason: string): IrrlichtPanelView
export function viewFromResponse(outcome: {
  response?: HttpResponse
  error?: unknown
  hostPort: string
  hereSessionId?: string
  nowMs: number
}): IrrlichtPanelView
