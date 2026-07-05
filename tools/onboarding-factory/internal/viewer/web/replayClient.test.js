import { describe, test, expect, afterEach } from 'vitest'
import {
  startReplay, replayStatus, setReplaySpeed, pauseReplay, resumeReplay, stopReplay, seekReplay,
} from './replayClient.js'

const origFetch = global.fetch
afterEach(() => { global.fetch = origFetch })

// stubFetch records every call and returns whatever `impl` produces.
function stubFetch(impl) {
  const calls = []
  global.fetch = (...args) => { calls.push(args); return impl(...args) }
  return calls
}

describe('startReplay', () => {
  test('POSTs the params as JSON and returns the parsed body on success', async () => {
    const calls = stubFetch(async () => ({ ok: true, status: 200, json: async () => ({ total_ms: 1000 }) }))
    const res = await startReplay({ agent: 'claudecode', subtree: 'core', scenario: 'x', speed: 2, recording: '' })
    expect(calls[0][0]).toBe('/api/replay/start')
    expect(calls[0][1].method).toBe('POST')
    expect(JSON.parse(calls[0][1].body)).toEqual({ agent: 'claudecode', subtree: 'core', scenario: 'x', speed: 2, recording: '' })
    expect(res).toEqual({ ok: true, status: 200, error: '', body: { total_ms: 1000 } })
  })

  test('surfaces status + error text on failure (no body)', async () => {
    stubFetch(async () => ({ ok: false, status: 404, text: async () => 'no recording' }))
    const res = await startReplay({ agent: 'a' })
    expect(res).toEqual({ ok: false, status: 404, error: 'no recording', body: null })
  })
})

describe('replayStatus', () => {
  test('GETs status and returns the parsed snapshot', async () => {
    const calls = stubFetch(async () => ({ json: async () => ({ active: true, offset_ms: 42 }) }))
    const st = await replayStatus()
    expect(calls[0][0]).toBe('/api/replay/status')
    expect(st).toEqual({ active: true, offset_ms: 42 })
  })
})

describe('control endpoints', () => {
  test('setReplaySpeed hits /speed with the multiplier', () => {
    const calls = stubFetch(() => Promise.resolve())
    setReplaySpeed(5)
    expect(calls[0]).toEqual(['/api/replay/speed?speed=5', { method: 'POST' }])
  })

  test('pause / resume / stop POST their endpoints', () => {
    const calls = stubFetch(() => Promise.resolve())
    pauseReplay(); resumeReplay(); stopReplay()
    expect(calls.map(c => c[0])).toEqual(['/api/replay/pause', '/api/replay/resume', '/api/replay/stop'])
    expect(calls.every(c => c[1].method === 'POST')).toBe(true)
  })

  test('seekReplay hits /seek with the absolute offset', () => {
    const calls = stubFetch(() => Promise.resolve())
    seekReplay(1234)
    expect(calls[0]).toEqual(['/api/replay/seek?offset_ms=1234', { method: 'POST' }])
  })
})
