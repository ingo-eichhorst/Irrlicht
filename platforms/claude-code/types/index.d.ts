// The irrlicht-panel mod's `$.state` contract: the values its hooks module
// writes and its Pane draws from. The shapes are what lib/view.js builds;
// lib/view.test.js checks a built view round-trips through JSON, since the
// host keeps these values as plain data.

/** One quota window of the bucket shown: label, percent used, bar tone, time to reset. */
export type IrrlichtPanelWindow = {
  label: string
  usedPercent: number
  tone: 'green' | 'yellow' | 'orange'
  resetsIn: string
}

/** The one quota bucket the panel shows (lib/quota.js selectBucket). */
export type IrrlichtPanelQuota = {
  title: string
  windows: IrrlichtPanelWindow[]
  isStale: boolean
  retrievalFailure: string
}

/** What pressing a session's row reveals. */
export type IrrlichtPanelDetail = {
  state: string
  elapsed: string
  cost: string
  textKind: '' | 'question' | 'last reply'
  text: string
}

/** One session row; `depth` is 1 for a child (subagent) of the row above. */
export type IrrlichtPanelRow = {
  id: string
  depth: number
  state: string
  color: string
  branch: string
  tokens: string
  context: string
  model: string
  adapterTag: string
  isHere: boolean
  detail: IrrlichtPanelDetail
}

/** One project group's rows. */
export type IrrlichtPanelSection = {
  name: string
  count: number
  rows: IrrlichtPanelRow[]
}

/** One poll's outcome: the daemon's sessions, or why it could not be read. */
export type IrrlichtPanelView =
  | {
      status: 'ok'
      hostPort: string
      quota: IrrlichtPanelQuota | null
      sections: IrrlichtPanelSection[]
      sessionCount: number
      dayCost: string
    }
  | { status: 'unreachable'; hostPort: string; reason: string }

declare module 'claude-code' {
  interface PluginState {
    'irrlicht-panel': {
      /** The last poll's view; null until the first poll answers. */
      view: IrrlichtPanelView | null
      /** Session ids whose detail block is open. */
      expanded: string[]
    }
  }
}
