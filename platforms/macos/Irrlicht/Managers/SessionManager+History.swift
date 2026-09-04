import Foundation

// MARK: - History bar decoding
//
// Split out of SessionManager.swift (#807): the wire format for the
// menu-bar's per-session state-history strip. All three granularities (1s,
// 10s, 60s) arrive continuously over the WebSocket; this just decodes the
// one-byte-per-bucket wire encoding and maintains the ring buffers, so
// `setHistoryGranularity` is a constant-time mirror rather than a re-fetch.
//
// State names here are plain `String` (not `SessionState.State`) because the
// wire format needs a value — `""` for "no data yet" — that isn't a real
// session state.
//
// #1805 replaced the original 2-bit packing with one byte per bucket, which is
// what let `error` join ready/working/waiting. Codes are UInt8, not Int8: the
// no-data sentinel is 255, and the daemon's own encoder returns an unsigned
// type so a negative can no longer reach the wire at all.

extension SessionManager {
    /// Selects which granularity the history bar renders (1, 10, or 60 s).
    /// All three streams arrive continuously over the WebSocket, so this is a
    /// constant-time mirror — no polling kick-off, no cancellation needed.
    func setHistoryGranularity(_ granularitySec: Int) {
        guard [1, 10, 60].contains(granularitySec) else { return }
        currentHistoryGranularitySec = granularitySec
        refreshActiveStateHistory()
    }

    func refreshActiveStateHistory() {
        let active = historyByGranularity[currentHistoryGranularitySec] ?? [:]
        // Strip leading no-data buckets so HistoryBarView's right-anchored
        // rendering leaves the front of the bar empty (matches pre-WS shape).
        stateHistory = active.mapValues { trimLeadingNoData($0) }
    }

    func trimLeadingNoData(_ buckets: [String]) -> [String] {
        var i = 0
        while i < buckets.count && buckets[i].isEmpty { i += 1 }
        return Array(buckets[i...])
    }

    /// Maps a wire code back to its state name.
    /// `""` represents no-data; HistoryBarView treats it as a blank slot.
    ///
    /// Anything off the ladder — the 255 no-data sentinel, or a code from a
    /// newer daemon — falls to `""` and paints nothing. It must never fall to a
    /// real state: painting an unknown code as `ready` is the green-error bug
    /// #1807 removed, and blank is the honest reading of a code this build
    /// cannot name.
    func historyPriorityToState(_ p: UInt8) -> String {
        switch p {
        case 0: return "ready"
        case 1: return "working"
        case 2: return "waiting"
        case 3: return "error"
        default: return ""
        }
    }

    func historyPriorityForState(_ s: String) -> Int {
        switch s {
        case "error":   return 3
        case "waiting": return 2
        case "working": return 1
        case "ready":   return 0
        default:        return -1 // no-data — strictly less than any real priority
        }
    }

    /// Decodes an 80-char base64 (60 bytes, one code per bucket) into a
    /// 60-element oldest→newest state-name array. Returns nil on malformed
    /// input so the caller can drop the message rather than corrupt the ring.
    ///
    /// The length check stays HARD. A payload of the wrong size is exactly how
    /// an older daemon's 15-byte packing arrives here, and dropping the whole
    /// message leaves the strip blank; to decode it partially would invent
    /// buckets. Never relax this into a prefix read.
    func decodeHistoryBuckets(_ encoded: String) -> [String]? {
        guard let raw = Data(base64Encoded: encoded), raw.count == 60 else { return nil }
        return raw.map { historyPriorityToState($0) }
    }

    func applyHistorySnapshot(sessionID: String, history: [String: String], generations: [String: UInt64]?) {
        for (granKey, b64) in history {
            guard let gran = Int(granKey),
                  [1, 10, 60].contains(gran),
                  let buckets = decodeHistoryBuckets(b64) else { continue }
            historyByGranularity[gran, default: [:]][sessionID] = buckets
        }
        // Seed the dedup high-water-mark from the snapshot's generations so
        // any tick already reflected in this snapshot gets skipped on arrival.
        if let gens = generations {
            var perGran = lastTickGen[sessionID] ?? [:]
            for (granKey, gen) in gens {
                if let gran = Int(granKey), [1, 10, 60].contains(gran) {
                    perGran[gran] = gen
                }
            }
            lastTickGen[sessionID] = perGran
        }
        refreshActiveStateHistory()
    }

    func applyHistoryTick(granularitySec: Int, buckets: [String: UInt8], bucketGenerations: [String: UInt64]?) {
        guard [1, 10, 60].contains(granularitySec) else { return }
        var dict = historyByGranularity[granularitySec] ?? [:]
        var changedActive = false
        for (sid, prio) in buckets {
            // Skip if this tick has already been folded into our snapshot.
            if let gen = bucketGenerations?[sid] {
                let last = lastTickGen[sid]?[granularitySec] ?? 0
                if gen <= last { continue }
                var perGran = lastTickGen[sid] ?? [:]
                perGran[granularitySec] = gen
                lastTickGen[sid] = perGran
            }
            var arr = dict[sid] ?? Array(repeating: "", count: 60)
            if arr.count == 60 { arr.removeFirst() }
            arr.append(historyPriorityToState(prio))
            // Pad to 60 if a previously-unknown session ticks before its snapshot.
            while arr.count < 60 { arr.insert("", at: 0) }
            dict[sid] = arr
            changedActive = true
        }
        historyByGranularity[granularitySec] = dict
        if changedActive && granularitySec == currentHistoryGranularitySec {
            refreshActiveStateHistory()
        }
    }

    func applyHistoryUpgrade(sessionID: String, priority: UInt8) {
        let newState = historyPriorityToState(priority)
        let newPrio = historyPriorityForState(newState)
        var changedActive = false
        for gran in [1, 10, 60] {
            var dict = historyByGranularity[gran] ?? [:]
            guard var arr = dict[sessionID], !arr.isEmpty else { continue }
            let lastPrio = historyPriorityForState(arr[arr.count - 1])
            if newPrio > lastPrio {
                arr[arr.count - 1] = newState
                dict[sessionID] = arr
                historyByGranularity[gran] = dict
                if gran == currentHistoryGranularitySec { changedActive = true }
            }
        }
        if changedActive {
            refreshActiveStateHistory()
        }
    }
}
