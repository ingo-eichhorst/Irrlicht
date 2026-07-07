import SwiftUI

/// Editor for backchannel event→action rules (issue #724). Loads the rule set
/// from the daemon, lets the user pick an event and one or more responses, and
/// saves changes back (debounced). Shown only when the backchannel master
/// toggle is on.
@MainActor
final class BackchannelRulesModel: ObservableObject {
    @Published var rules: [BackchannelRule] = []

    func load() async {
        if let r = await BackchannelRulesClient.fetch() { rules = r }
    }

    func save() async {
        await BackchannelRulesClient.save(rules)
    }

    func addRule() {
        rules.append(BackchannelRule(
            id: UUID().uuidString,
            enabled: true,
            name: "New rule",
            trigger: .init(event: BackchannelRule.eventContextPressure, threshold: 85),
            actions: [.init(kind: BackchannelRule.actionInput, preset: BackchannelRule.presetCompact)],
            adapter: nil,
            cooldownSeconds: nil
        ))
    }

    func delete(id: String) {
        rules.removeAll { $0.id == id }
    }
}

private struct EventOption: Identifiable {
    let id: String
    let label: String
}

struct BackchannelRulesView: View {
    @StateObject private var model = BackchannelRulesModel()
    @State private var saveTask: Task<Void, Never>? = nil

    private let events: [EventOption] = [
        EventOption(id: BackchannelRule.eventWaiting, label: "Waiting"),
        EventOption(id: BackchannelRule.eventReady, label: "Ready"),
        EventOption(id: BackchannelRule.eventWorking, label: "Working"),
        EventOption(id: BackchannelRule.eventContextPressure, label: "Context (%)"),
        EventOption(id: BackchannelRule.eventContextTokens, label: "Context (tokens)"),
    ]

    /// The two context-pressure events whose Threshold field is a percentage vs
    /// an absolute token count. Drives the threshold-row defaults + unit suffix.
    private func isContextEvent(_ event: String) -> Bool {
        event == BackchannelRule.eventContextPressure || event == BackchannelRule.eventContextTokens
    }

    /// Default threshold for a context event's unit — mirrors the daemon
    /// (DefaultPressureThreshold / DefaultPressureTokens) and the Settings
    /// notification defaults (ContextPressureThreshold).
    private func contextDefault(for event: String) -> Double {
        event == BackchannelRule.eventContextTokens ? 150_000 : 85
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 6) {
                Text("Rules").font(.subheadline).fontWeight(.medium)
                InfoIcon(text: "Auto-send a response when an event fires on a controllable session — e.g. on context pressure, send /compact.")
                Spacer()
                Button {
                    model.addRule()
                    scheduleSave()
                } label: {
                    Image(systemName: "plus.circle")
                }
                .buttonStyle(.borderless)
            }

            if model.rules.isEmpty {
                Text("No rules yet. Add one to auto-respond to a session event.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }

            ForEach($model.rules) { $rule in
                ruleCard(rule: $rule)
            }
        }
        .task { await model.load() }
        .onChange(of: model.rules) { _ in scheduleSave() }
    }

    @ViewBuilder
    private func ruleCard(rule: Binding<BackchannelRule>) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                Toggle("", isOn: rule.enabled).labelsHidden().fixedSize()
                TextField("Name", text: Binding(
                    get: { rule.wrappedValue.name ?? "" },
                    set: { rule.wrappedValue.name = $0 }
                ))
                .textFieldStyle(.roundedBorder)
                Spacer()
                Button(role: .destructive) {
                    model.delete(id: rule.wrappedValue.id)
                    scheduleSave()
                } label: {
                    Image(systemName: "trash")
                }
                .buttonStyle(.borderless)
            }

            // Event, threshold (context events only), and Agent each on their
            // own row. The .menu pickers are .fixedSize() (they won't compress),
            // so crowding them into one HStack overflowed the ~288pt card and
            // clipped the whole Settings panel (#724 layout fix). The threshold
            // gets its own row so the wider "Context (tokens)" picker + value +
            // unit never force the labels to wrap.
            HStack(spacing: 6) {
                Text("When").font(.caption).foregroundColor(.secondary)
                Picker("", selection: rule.trigger.event) {
                    ForEach(events) { ev in
                        Text(ev.label).tag(ev.id)
                    }
                }
                .labelsHidden()
                .fixedSize()
                .onChange(of: rule.wrappedValue.trigger.event) { newEvent in
                    // Reset the threshold to the new event's unit default so a
                    // percentage (85) never lingers as a token count or vice-versa
                    // (mirrors ContextThresholdRow's unit-switch reset).
                    if isContextEvent(newEvent) {
                        rule.wrappedValue.trigger.threshold = contextDefault(for: newEvent)
                    }
                }
                Spacer()
            }

            if isContextEvent(rule.wrappedValue.trigger.event) {
                let isTokens = rule.wrappedValue.trigger.event == BackchannelRule.eventContextTokens
                let fallback = contextDefault(for: rule.wrappedValue.trigger.event)
                HStack(spacing: 6) {
                    Text("≥").font(.caption).foregroundColor(.secondary)
                    TextField(isTokens ? "150000" : "85", value: Binding(
                        get: { rule.wrappedValue.trigger.threshold ?? fallback },
                        set: { rule.wrappedValue.trigger.threshold = $0 }
                    ), format: .number)
                    .frame(width: isTokens ? 72 : 48)
                    .textFieldStyle(.roundedBorder)
                    Text(isTokens ? "tokens" : "%").font(.caption).foregroundColor(.secondary)
                    Spacer()
                }
            }

            HStack(spacing: 6) {
                Text("Agent").font(.caption).foregroundColor(.secondary)
                Picker("", selection: rule.adapter) {
                    Text("Any").tag(String?.none)
                    ForEach(agents, id: \.name) { a in
                        Text(a.displayName).tag(Optional(a.name))
                    }
                }
                .labelsHidden()
                .fixedSize()
                Spacer()
            }

            ForEach(rule.wrappedValue.actions.indices, id: \.self) { i in
                actionRow(action: rule.actions[i], adapter: rule.wrappedValue.adapter) {
                    rule.wrappedValue.actions.remove(at: i)
                    scheduleSave()
                }
            }

            Button {
                rule.wrappedValue.actions.append(.init(kind: BackchannelRule.actionInput, data: ""))
                scheduleSave()
            } label: {
                Label("Add response", systemImage: "plus").font(.caption)
            }
            .buttonStyle(.borderless)
        }
        .padding(8)
        .background(RoundedRectangle(cornerRadius: 6).fill(Color.secondary.opacity(0.08)))
    }

    /// Agents known to the daemon, for the per-rule Agent scope picker. Empty
    /// until the branding registry hydrates (then "Any" is the only option).
    private var agents: [AgentBranding] {
        AgentRegistry.byName.values.sorted { $0.displayName < $1.displayName }
    }

    /// A warning when a rule scoped to a specific agent selects a preset that
    /// agent doesn't support — the daemon won't fire it (issue #754). nil when
    /// supported, or when the rule targets any agent (it fires where supported).
    private func unsupportedWarning(preset: String, adapter: String?) -> String? {
        guard let adapter, !adapter.isEmpty,
              let branding = AgentRegistry.byName[adapter] else { return nil }
        if branding.supportedPresets.contains(preset) { return nil }
        return "Not supported by \(branding.displayName)"
    }

    @ViewBuilder
    private func actionRow(action: Binding<BackchannelRule.Action>, adapter: String?, onDelete: @escaping () -> Void) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Picker("", selection: action.kind) {
                    Text("Send").tag(BackchannelRule.actionInput)
                    Text("Interrupt").tag(BackchannelRule.actionInterrupt)
                }
                .labelsHidden()
                .fixedSize()

                if action.wrappedValue.kind == BackchannelRule.actionInput {
                    // Preset vs Custom: the empty tag is Custom (preset nil), which
                    // reveals the raw-text field — exactly today's behavior.
                    Picker("", selection: Binding(
                        get: { action.wrappedValue.preset ?? "" },
                        set: { action.wrappedValue.preset = $0.isEmpty ? nil : $0 }
                    )) {
                        ForEach(BackchannelRule.presetCatalog, id: \.id) { p in
                            Text(p.label).tag(p.id)
                        }
                        Text("Custom").tag("")
                    }
                    .labelsHidden()
                    .fixedSize()

                    if action.wrappedValue.preset == nil {
                        TextField("/compact", text: Binding(  // NOSONAR (swift:S1075) — slash-command placeholder text, not a URI
                            get: { action.wrappedValue.data ?? "" },
                            set: { action.wrappedValue.data = $0 }
                        ))
                        .textFieldStyle(.roundedBorder)
                    } else {
                        Spacer()
                    }
                } else {
                    Spacer()
                }

                Button(role: .destructive, action: onDelete) {
                    Image(systemName: "minus.circle")
                }
                .buttonStyle(.borderless)
            }

            // Unsupported-preset warning on its own row so a long agent name
            // can't widen (and clip) the whole Settings panel.
            if action.wrappedValue.kind == BackchannelRule.actionInput,
               let preset = action.wrappedValue.preset,
               let warning = unsupportedWarning(preset: preset, adapter: adapter) {
                HStack(spacing: 4) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .foregroundColor(.orange)
                        .font(.caption)
                    Text(warning)
                        .font(.caption)
                        .foregroundColor(.orange)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }

    /// Debounced save so per-keystroke edits don't PUT on every character.
    private func scheduleSave() {
        saveTask?.cancel()
        saveTask = Task {
            try? await Task.sleep(nanoseconds: 600_000_000)
            if Task.isCancelled { return }
            await model.save()
        }
    }
}

extension BackchannelRulesView {
    /// Injects a pre-populated model for snapshot tests — the view otherwise
    /// hydrates its rules from the daemon (unreachable under test). Defined in an
    /// extension so the synthesized `init()` production uses stays available.
    init(model: BackchannelRulesModel) {
        _model = StateObject(wrappedValue: model)
    }
}
