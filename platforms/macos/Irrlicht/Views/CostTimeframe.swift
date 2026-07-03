import Foundation

// MARK: - Cost Timeframe

enum CostTimeframe: String, CaseIterable {
    case day, week, month, year

    var suffix: String {
        switch self {
        case .day:   return " / day"
        case .week:  return " / week"
        case .month: return " / month"
        case .year:  return " / year"
        }
    }

    static func from(_ raw: String) -> CostTimeframe {
        CostTimeframe(rawValue: raw) ?? .day
    }

    func next() -> CostTimeframe {
        let all = Self.allCases
        let idx = all.firstIndex(of: self) ?? 0
        return all[(idx + 1) % all.count]
    }
}
