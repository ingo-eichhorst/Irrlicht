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
            actions: [.init(kind: BackchannelRule.actionInput, data: "/compact")],
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
        EventOption(id: BackchannelRule.eventContextPressure, label: "Context pressure"),
    ]

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

            HStack(spacing: 6) {
                Text("When").font(.caption).foregroundColor(.secondary)
                Picker("", selection: rule.trigger.event) {
                    ForEach(events) { ev in
                        Text(ev.label).tag(ev.id)
                    }
                }
                .labelsHidden()
                .fixedSize()

                if rule.wrappedValue.trigger.event == BackchannelRule.eventContextPressure {
                    Text("≥").font(.caption).foregroundColor(.secondary)
                    TextField("85", value: Binding(
                        get: { rule.wrappedValue.trigger.threshold ?? 85 },
                        set: { rule.wrappedValue.trigger.threshold = $0 }
                    ), format: .number)
                    .frame(width: 48)
                    .textFieldStyle(.roundedBorder)
                    Text("%").font(.caption).foregroundColor(.secondary)
                }
                Spacer()
            }

            ForEach(rule.wrappedValue.actions.indices, id: \.self) { i in
                actionRow(action: rule.actions[i]) {
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

    @ViewBuilder
    private func actionRow(action: Binding<BackchannelRule.Action>, onDelete: @escaping () -> Void) -> some View {
        HStack(spacing: 6) {
            Picker("", selection: action.kind) {
                Text("Send").tag(BackchannelRule.actionInput)
                Text("Interrupt").tag(BackchannelRule.actionInterrupt)
            }
            .labelsHidden()
            .fixedSize()

            if action.wrappedValue.kind == BackchannelRule.actionInput {
                TextField("/compact", text: Binding(
                    get: { action.wrappedValue.data ?? "" },
                    set: { action.wrappedValue.data = $0 }
                ))
                .textFieldStyle(.roundedBorder)
            } else {
                Spacer()
            }

            Button(role: .destructive, action: onDelete) {
                Image(systemName: "minus.circle")
            }
            .buttonStyle(.borderless)
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
