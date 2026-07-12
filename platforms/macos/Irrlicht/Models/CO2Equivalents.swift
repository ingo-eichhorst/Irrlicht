import Foundation

/// A relatable everyday CO2e activity used as a reference line on the History
/// CO2 chart (issue #952) — mirrors the web `CO2_EQUIVALENTS`
/// (platforms/web/historyTab.js). Kept ascending by `grams`; `id` is a stable
/// string (never reused across entries) so `pick(maxY:)` can track
/// already-chosen entries by id instead of by reference identity, which Swift
/// structs don't have.
struct CO2Equivalent: Equatable, Identifiable {
    let id: String
    let grams: Double
    let label: String
}

enum CO2Equivalents {
    /// Kept in lockstep with the web `CO2_EQUIVALENTS` — same ids, grams, and
    /// labels, updated together in the same change. Every figure's citation
    /// lives on the "CO2 Methodology" docs page, not duplicated here.
    static let all: [CO2Equivalent] = [
        CO2Equivalent(id: "search", grams: 0.2, label: "a web search"),
        CO2Equivalent(id: "phone-charge", grams: 10, label: "charging a smartphone"),
        CO2Equivalent(id: "stream-hour", grams: 36, label: "1 hour of video streaming"),
        CO2Equivalent(id: "kettle", grams: 60, label: "boiling a kettle"),
        CO2Equivalent(id: "car-km", grams: 170, label: "driving 1 km by car"),
        CO2Equivalent(id: "grid-kwh", grams: 460, label: "1 kWh of average grid electricity"),
        CO2Equivalent(id: "shower", grams: 1_000, label: "a hot shower"),
        CO2Equivalent(id: "laundry", grams: 1_500, label: "a load of laundry"),
        CO2Equivalent(id: "petrol-liter", grams: 2_350, label: "burning 1 liter of petrol"),
        CO2Equivalent(id: "bike-frame", grams: 5_500, label: "manufacturing a bicycle frame"),
        CO2Equivalent(id: "running-shoes", grams: 9_500, label: "manufacturing a pair of running shoes"),
        CO2Equivalent(id: "jeans", grams: 33_400, label: "a pair of jeans, cradle to grave"),
        CO2Equivalent(id: "flight-short", grams: 43_800, label: "a short-haul flight (London → Paris)"),
        CO2Equivalent(id: "tree-year", grams: 60_000, label: "a tree's CO2 absorption for a year"),
        CO2Equivalent(id: "car-commute-month", grams: 118_000, label: "a month of average car commuting"),
        CO2Equivalent(id: "laptop", grams: 185_000, label: "a laptop, cradle to grave"),
        CO2Equivalent(id: "flight-long", grams: 650_000, label: "a long-haul flight (London → New York)"),
        CO2Equivalent(id: "flight-long-return", grams: 1_300_000, label: "a round-trip long-haul flight (there and back)"),
        CO2Equivalent(id: "car-year", grams: 4_290_000, label: "an average car's emissions for a year"),
        CO2Equivalent(id: "person-year", grams: 4_800_000, label: "an average person's annual carbon footprint"),
        CO2Equivalent(id: "cars-9t", grams: 8_580_000, label: "roughly 2 average cars' annual emissions"),
        CO2Equivalent(id: "cars-13t", grams: 12_870_000, label: "roughly 3 average cars' annual emissions"),
        CO2Equivalent(id: "cars-25t", grams: 25_000_000, label: "roughly 6 average cars' annual emissions"),
        CO2Equivalent(id: "people-100t", grams: 100_000_000, label: "roughly 21 people's average annual carbon footprint"),
    ]

    /// Log-scale fractions of the axis maximum, aimed one-per-line — mirrors
    /// the web `co2EquivalentTargets`. Deliberately wide spread (0.04/0.2/0.8,
    /// not evenly spaced) so the picks read as low/mid/high scale rather than
    /// clustering in the middle of the visible range.
    static func targets(candidateCount: Int) -> [Double] {
        if candidateCount >= 3 { return [0.04, 0.2, 0.8] }
        if candidateCount == 2 { return [0.1, 0.7] }
        return [0.4]
    }

    /// Whichever candidate not in `picked` sits closest, in log-space, to
    /// `targetLog` — mirrors the web `nearestUnpickedEquivalent`. `candidates`
    /// is always ascending-by-grams (filtered from `all` without reordering),
    /// so a strict `<` comparison preserves the JS tie-break: the first
    /// (smallest) candidate encountered wins ties.
    static func nearestUnpicked(in candidates: [CO2Equivalent], picked: Set<String>, targetLog: Double) -> CO2Equivalent? {
        var best: CO2Equivalent?
        var bestDist = Double.infinity
        for eq in candidates {
            if picked.contains(eq.id) { continue }
            let dist = abs(log(eq.grams) - targetLog)
            if dist < bestDist { bestDist = dist; best = eq }
        }
        return best
    }

    /// Chooses up to 3 reference lines that sit inside `0..<maxY*0.98` —
    /// mirrors the web `pickCO2Equivalents` 1:1. Every `all` entry has
    /// `grams > 0`, and `maxY > 0` is required by the guard below, so
    /// `log(maxY * frac)` (frac always > 0) is always a finite argument —
    /// `log(0)`/negative inputs are unreachable.
    static func pick(maxY: Double) -> [CO2Equivalent] {
        guard maxY > 0 else { return [] }
        let ceiling = maxY * 0.98
        let candidates = all.filter { $0.grams > 0 && $0.grams < ceiling }
        guard !candidates.isEmpty else { return [] }
        var picked: [CO2Equivalent] = []
        var pickedIds: Set<String> = []
        for frac in targets(candidateCount: candidates.count) {
            guard let best = nearestUnpicked(in: candidates, picked: pickedIds, targetLog: log(maxY * frac)) else { continue }
            picked.append(best)
            pickedIds.insert(best.id)
        }
        return picked.sorted { $0.grams < $1.grams }
    }
}
