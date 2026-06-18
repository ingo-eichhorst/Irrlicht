import Foundation

/// The user-configurable threshold at which a session's context-fill alert
/// fires — both the in-app row badge and the desktop notification. A single
/// threshold, entered either as a percentage of the context window or as an
/// absolute token count (issue #689).
struct ContextPressureThreshold: Equatable {
    enum Unit: String {
        case percent
        case tokens
    }

    var value: Double
    var unit: Unit

    static let defaultValue: Double = 80
    static let defaultUnit: Unit = .percent

    static let valueKey = "contextPressureThresholdValue"
    static let unitKey = "contextPressureThresholdUnit"

    /// A sensible starting value for each unit, used when the user switches the
    /// unit in Settings (so percent→tokens doesn't leave "80 tokens" behind).
    static func defaultValue(for unit: Unit) -> Double {
        switch unit {
        case .percent: return 80
        case .tokens: return 150_000
        }
    }

    /// Snapshot of the current config from `UserDefaults`. Falls back to the
    /// defaults when unset or non-positive, so callers never read a 0 threshold
    /// that would alert on every session.
    static var current: ContextPressureThreshold {
        let defaults = UserDefaults.standard
        let raw = defaults.double(forKey: valueKey)
        let value = raw > 0 ? raw : defaultValue
        let unit = Unit(rawValue: defaults.string(forKey: unitKey) ?? "") ?? defaultUnit
        return ContextPressureThreshold(value: value, unit: unit)
    }

    /// True once a session's context usage has reached the configured threshold.
    /// Pure (no `UserDefaults`) so it is directly unit-testable. In percent mode
    /// an unknown context window leaves `contextUtilization` at 0 and never
    /// fires; token mode keys off the raw count and works regardless.
    func isExceeded(by metrics: SessionMetrics) -> Bool {
        switch unit {
        case .percent:
            return metrics.contextUtilization > 0 && metrics.contextUtilization >= value
        case .tokens:
            return metrics.totalTokens > 0 && Double(metrics.totalTokens) >= value
        }
    }
}
