import ServiceManagement
import SwiftUI
import UniformTypeIdentifiers
import UserNotifications

struct SettingsView: View {
    /// Placeholder/default relay address shown until the user sets their own
    /// (SonarQube swift:S1075 flags the literal; this is intentionally fixed
    /// — it's an example value, not something worth threading through as a
    /// parameter for its three call sites here).
    private static let defaultRelayURLPlaceholder = "ws://localhost:7839"  // NOSONAR (swift:S1075) — see doc comment above

    @Binding var isPresented: Bool
    /// Swaps the panel body to the permission wizard in review mode (issue #570).
    /// A binding (not a closure) so SettingsView's inputs stay diff-stable — the
    /// parent re-renders every WebSocket tick, and a fresh closure each time would
    /// defeat SwiftUI's render-skip and re-evaluate this whole view (issue #729).
    @Binding var showPermissionsReview: Bool
    @EnvironmentObject var updateManager: UpdateManager
    /// Plain, non-observing reference: SettingsView only calls
    /// `relayTokenDidChange()` and feeds the relay status dot subview. Observing
    /// SessionManager here (it was an @EnvironmentObject) made this static form
    /// re-render on every session/history push — the ~2fps storm in issue #729.
    let sessionManager: SessionManager
    @EnvironmentObject var daemonManager: DaemonManager
    @AppStorage("debugMode") private var debugMode: Bool = false
    @AppStorage("showCostDisplay") private var showCostDisplay: Bool = false
    @AppStorage("showQuotaForecast") private var showQuotaForecast: Bool = true
    @AppStorage("launchAtLogin") private var launchAtLogin: Bool = true
    @AppStorage("taskEtaActivation") private var taskEtaActivation: Bool = false
    // Backchannel master toggle (issue #724): gates input injection + the
    // event→action rule editor. Default OFF; daemon is the source of truth.
    @AppStorage("backchannelActivation") private var backchannelActivation: Bool = false
    // User-intent display (beta): surface each session's task summary as a
    // purple block in the sidebar. Pure UI preference — no daemon. Default OFF.
    @AppStorage("userIntentDisplay") private var userIntentDisplay: Bool = false
    // Advanced Settings disclosure state (#694) — collapsed by default; the
    // power-user / still-maturing controls (debug, task-eta, sources, CLI tool)
    // live under it.
    @AppStorage("advancedSettingsExpanded") private var advancedSettingsExpanded: Bool = false
    @AppStorage("providerMode_anthropic") private var providerModeAnthropic: String = ProviderModePreference.auto.rawValue
    @AppStorage("providerMode_openai") private var providerModeOpenAI: String = ProviderModePreference.auto.rawValue
    // Menu bar icon content (issue #909): dots / quota bars / both. Default
    // .lights keeps today's icon unchanged for existing users.
    @AppStorage(MenuBarStyle.storageKey) private var menuBarStyle: String = MenuBarStyle.lights.rawValue
    @AppStorage(MenuBarQuotaProvider.storageKey) private var menuBarQuotaProvider: String = ""
    @AppStorage(QuotaVisualStyle.storageKey) private var menuBarQuotaVisual: String = QuotaVisualStyle.bars.rawValue
    // Master gate (issue #940): collapses the whole Notifications section
    // when off, instead of showing three always-visible event rows to users
    // who don't want any of it. A brand-new key defaults to false, but
    // `reconcileNotificationsMasterDefault()` flips it true once on first
    // appearance for anyone upgrading with an event already enabled, so
    // existing notification setups don't silently vanish.
    @AppStorage("notificationsEnabled") private var notificationsEnabled: Bool = false
    @AppStorage(NotificationEvent.ready.enabledKey) private var notifyOnReady: Bool = false
    @AppStorage(NotificationEvent.waiting.enabledKey) private var notifyOnWaiting: Bool = false
    @AppStorage(NotificationEvent.contextPressure.enabledKey) private var notifyOnContextPressure: Bool = false
    // Sources (multi-source): mirror the web dashboard's keys.
    @AppStorage("useLocalDaemon") private var useLocalDaemon: Bool = true
    @AppStorage("useRelayServer") private var useRelayServer: Bool = false
    // Publish direction (issue #718): forward this Mac's sessions to the relay.
    // Independent of useRelayServer (subscribe); both share relayServerURL + the
    // Keychain token.
    @AppStorage("publishToRelay") private var publishToRelay: Bool = false
    @AppStorage("relayServerURL") private var relayServerURL: String = ""
    @State private var relayURLDraft: String = ""
    @State private var relayURLDebounceTask: Task<Void, Never>? = nil
    // Live publish-link state, polled from the daemon's /api/v1/relay/publish.
    @StateObject private var publishStatus = PublishStatusMonitor()
    // Set just before a programmatic reconcile of taskEtaActivation so the
    // .onChange it triggers is swallowed instead of re-POSTing to the daemon.
    @State private var taskEtaReconciling = false
    // Same reconcile guard for the backchannel toggle.
    @State private var backchannelReconciling = false
    // Relay bearer token lives in the Keychain (not @AppStorage); this draft
    // mirrors it for the field, loaded on appear and written on change.
    @State private var relayTokenDraft: String = ""
    @State private var notificationsDenied = false
    @State private var loginItemStatus: SMAppService.Status = .notRegistered
    @State private var customImportError: String?

    var body: some View {
        // Header and footer are pinned; only the settings sections scroll.
        // This keeps the footer visible no matter how many sections expand —
        // the old fixed `height: 860` frame overflowed the menu-bar popover
        // and clipped it. The scroll area is capped the same way
        // SessionListView caps its list (see `groupListMaxHeight`).
        // Horizontal insets live on the scroll *content* only — NOT on the
        // outer container or the dividers (issue #940: dividers run full-bleed
        // everywhere, matching History) — so the ScrollView spans the full
        // panel width and its scroll track sits flush against the right edge,
        // while the text stays inset.
        VStack(alignment: .leading, spacing: 0) {
            PanelHeader(title: "Settings", onBack: { isPresented = false })

            Divider()

            ScrollView {
                VStack(alignment: .leading, spacing: IrrSpacing.sp4) {
                    LeadingToggle(
                        isOn: $showCostDisplay,
                        label: "Show Estimated Cost",
                        info: "Display estimated USD cost per session and per project group. Cost estimates are approximate."
                    )

                    VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
                        LeadingToggle(
                            isOn: $showQuotaForecast,
                            label: "Show Quota Forecast",
                            info: "Replace the app version in the header with a live burn-rate forecast against your Pro/Max or ChatGPT subscription cap. Only appears when a subscription session is active."
                        )

                        if showQuotaForecast {
                            HStack(spacing: 6) {
                                Text("Provider")
                                    .font(.caption)
                                    .fontWeight(.medium)
                                    .foregroundColor(.secondary)
                                InfoIcon(text: "Auto picks the chip variant from the snapshot; override when you have multiple paths into one provider.")
                                Spacer()
                            }
                            providerModeRow(label: "Anthropic", selection: $providerModeAnthropic)
                            providerModeRow(label: "OpenAI", selection: $providerModeOpenAI)
                        }
                    }

                    VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
                        HStack(spacing: 6) {
                            Text("Menu Bar Icon")
                                .font(.caption)
                                .fontWeight(.medium)
                                .foregroundColor(.secondary)
                            InfoIcon(text: "Lights shows session-state dots (today's default). Usage replaces them with 5h/7d subscription quota bars. Combined shows both side by side.")
                            Spacer()
                        }
                        Picker("", selection: $menuBarStyle) {
                            ForEach(MenuBarStyle.allCases) { style in
                                Text(style.label).tag(style.rawValue)
                            }
                        }
                        .pickerStyle(.segmented)
                        .labelsHidden()
                        // Tint with the app's accent instead of the system default
                        // blue, matching every other session-state color in the
                        // app (issue #940).
                        .tint(IrrColors.working)

                        if menuBarStyle != MenuBarStyle.lights.rawValue {
                            HStack(spacing: 6) {
                                Text("Quota provider")
                                    .font(.caption)
                                    .foregroundColor(.secondary)
                                Spacer()
                                Picker("", selection: $menuBarQuotaProvider) {
                                    Text("Auto").tag("")
                                    ForEach(knownQuotaProviderKeys, id: \.self) { key in
                                        Text(quotaProviderLabel(key)).tag(key)
                                    }
                                }
                                .labelsHidden()
                                .frame(maxWidth: 140)
                            }
                            HStack(spacing: 6) {
                                Text("Quota shape")
                                    .font(.caption)
                                    .foregroundColor(.secondary)
                                Spacer()
                                Picker("", selection: $menuBarQuotaVisual) {
                                    ForEach(QuotaVisualStyle.allCases) { visual in
                                        Text(visual.label).tag(visual.rawValue)
                                    }
                                }
                                .pickerStyle(.segmented)
                                .labelsHidden()
                                .frame(maxWidth: 140)
                                .tint(IrrColors.working)
                            }
                        }
                    }

                    VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
                        LeadingToggle(
                            isOn: $launchAtLogin,
                            label: "Open at Login",
                            info: "Start Irrlicht automatically when you log in to your Mac."
                        )
                        .onChange(of: launchAtLogin) { newValue in
                            LoginItemManager.setEnabled(newValue)
                            // setEnabled runs detached; give launchd a beat,
                            // then re-read the system's real status.
                            refreshLoginItemStatus(after: 0.4)
                        }

                        if loginItemStatus == .requiresApproval {
                            HStack(spacing: 4) {
                                Image(systemName: "exclamationmark.triangle.fill")
                                    .foregroundColor(.orange)
                                    .font(.caption)
                                    .tooltip("Login item needs approval in System Settings")
                                Text("Approve Irrlicht in Login Items.")
                                    .font(.caption)
                                    .foregroundColor(.orange)
                                Button("Open Login Items") {
                                    LoginItemManager.openLoginItemsSettings()
                                }
                                .font(.caption)
                                .buttonStyle(.link)
                                .tooltip("Open System Settings → General → Login Items")
                            }
                        }
                    }
                    .onAppear { refreshLoginItemStatus() }

                    Divider()

                    VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
                        HStack(spacing: 6) {
                            Text("Permissions")
                                .font(.subheadline)
                                .fontWeight(.medium)
                            InfoIcon(text: "Everything irrlicht may read or modify, per agent. Toggling a grant off undoes the modification and stops all reading.")
                            Spacer()
                        }
                        Button("Review agent permissions…") {
                            isPresented = false
                            showPermissionsReview = true
                        }
                        .controlSize(.small)
                        .tooltip("Open the per-agent permission toggles")
                    }

                    Divider()

                    VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
                        HStack(spacing: 6) {
                            Text("Notifications")
                                .font(.subheadline)
                                .fontWeight(.medium)
                            InfoIcon(text: "Pick a sound per event, choose your own audio file, or have the message read aloud.")
                            Spacer()
                        }

                        LeadingToggle(isOn: $notificationsEnabled, label: "Enable notifications")

                        if notificationsEnabled {
                            VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
                                NotificationEventRow(
                                    event: .ready,
                                    enabled: $notifyOnReady,
                                    sampleText: "Agent ready",
                                    onImportError: { customImportError = $0 }
                                )
                                NotificationEventRow(
                                    event: .waiting,
                                    enabled: $notifyOnWaiting,
                                    sampleText: "Agent waiting for input",
                                    onImportError: { customImportError = $0 }
                                )
                                NotificationEventRow(
                                    event: .contextPressure,
                                    enabled: $notifyOnContextPressure,
                                    sampleText: "Context pressure: 80% threshold reached",
                                    onImportError: { customImportError = $0 }
                                )
                                ContextThresholdRow(enabled: notifyOnContextPressure)
                                    .padding(.leading, IrrSpacing.sp4)
                            }

                            if let error = customImportError {
                                Text(error)
                                    .font(.caption)
                                    .foregroundColor(.orange)
                                    .fixedSize(horizontal: false, vertical: true)
                            }

                            if notificationsDenied && (notifyOnReady || notifyOnWaiting || notifyOnContextPressure) {
                                HStack(spacing: 4) {
                                    Image(systemName: "exclamationmark.triangle.fill")
                                        .foregroundColor(.orange)
                                        .font(.caption)
                                        .tooltip("Notifications blocked in System Settings")
                                    Text("Notifications are blocked.")
                                        .font(.caption)
                                        .foregroundColor(.orange)
                                    Button("Open Settings") {
                                        if let url = URL(string: "x-apple.systempreferences:com.apple.Notifications-Settings") {
                                            NSWorkspace.shared.open(url)
                                        }
                                    }
                                    .font(.caption)
                                    .buttonStyle(.link)
                                    .tooltip("Open System Settings → Notifications")
                                }
                            }
                        }
                    }
                    .onAppear {
                        reconcileNotificationsMasterDefault()
                        checkNotificationAuth()
                    }
                    .onChange(of: notificationsEnabled) { _ in checkNotificationAuth() }
                    .onChange(of: notifyOnReady) { _ in checkNotificationAuth() }
                    .onChange(of: notifyOnWaiting) { _ in checkNotificationAuth() }
                    .onChange(of: notifyOnContextPressure) { _ in checkNotificationAuth() }

                    Divider()

                    VStack(alignment: .leading, spacing: IrrSpacing.sp3) {
                        Text("Updates")
                            .font(.subheadline)
                            .fontWeight(.medium)

                        LeadingToggle(
                            isOn: $updateManager.automaticallyChecksForUpdates,
                            label: "Automatically check for updates"
                        )

                        HStack {
                            Text("Current version")
                                .font(.caption)
                                .foregroundColor(.secondary)
                            Spacer()
                            Text(appVersion)
                                .font(.caption)
                                .foregroundColor(.secondary)
                                .monospacedDigit()
                        }

                        HStack {
                            Text("Last checked")
                                .font(.caption)
                                .foregroundColor(.secondary)
                            Spacer()
                            Text(lastCheckedDescription)
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }

                        Button("Check for Updates…") {
                            updateManager.checkForUpdates()
                        }
                        .controlSize(.small)
                    }
                    .onAppear { updateManager.refresh() }

                    Divider()

                    // Advanced Settings (#694): power-user and still-maturing
                    // controls, collapsed by default. Disclosure state persists
                    // via @AppStorage("advancedSettingsExpanded").
                    VStack(alignment: .leading, spacing: IrrSpacing.sp3) {
                        Button {
                            withAnimation(IrrMotion.easeOut(duration: IrrMotion.fast)) {
                                advancedSettingsExpanded.toggle()
                            }
                        } label: {
                            HStack(spacing: 6) {
                                Image(systemName: "chevron.right")
                                    .font(.caption2.weight(.semibold))
                                    .foregroundColor(.secondary)
                                    .rotationEffect(.degrees(advancedSettingsExpanded ? 90 : 0))
                                Text("Advanced Settings")
                                    .font(.subheadline)
                                    .fontWeight(.medium)
                                Spacer()
                            }
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .tooltip("Show debug, task-estimate markers, sources, and the command-line tool")
                        .accessibilityElement(children: .combine)
                        .accessibilityValue(advancedSettingsExpanded ? "expanded" : "collapsed")

                        if advancedSettingsExpanded {
                            LeadingToggle(
                                isOn: $debugMode,
                                label: "Debug Mode",
                                info: "Show session IDs, creation time, and time since last update."
                            )

                            // Task-eta global activation (issue #558). The daemon is
                            // the source of truth: the toggle posts the flip and then
                            // mirrors the daemon's answer, so a failed install never
                            // shows as "on".
                            LeadingToggle(
                                isOn: $taskEtaActivation,
                                label: "Task-Estimate Markers",
                                info: "Add a managed emission rule to ~/.claude/CLAUDE.md so agents report task progress and sessions show a completion ETA. Only the Irrlicht-managed block is written; the rest of the file is untouched. Turning this off removes the block.",
                                beta: true
                            )
                            .onChange(of: taskEtaActivation) { newValue in
                                // Swallow the change we made ourselves while reconciling
                                // from the daemon — only a real user toggle should write.
                                if taskEtaReconciling { taskEtaReconciling = false; return }
                                Task {
                                    // nil = daemon unreachable: leave the toggle where the
                                    // user put it; the next open reconciles. Only correct
                                    // the toggle on a definite, differing answer.
                                    if let actual = await ActivationClient.set(enabled: newValue), actual != newValue {
                                        taskEtaReconciling = true
                                        taskEtaActivation = actual
                                    }
                                }
                            }
                            .onAppear {
                                Task {
                                    // Only reconcile on a definite answer — an unreachable
                                    // daemon (nil) must NOT flip the toggle off and trigger
                                    // a spurious uninstall of the managed CLAUDE.md block.
                                    if let actual = await ActivationClient.status(), actual != taskEtaActivation {
                                        taskEtaReconciling = true
                                        taskEtaActivation = actual
                                    }
                                }
                            }

                            // User-intent display (beta): show each session's task
                            // summary — the agent's irrlicht-summary marker, else its
                            // first user prompt (#738) — as a purple block in the
                            // sidebar, mirroring the orange "waiting" question. Pure UI
                            // preference; no daemon involvement.
                            LeadingToggle(
                                isOn: $userIntentDisplay,
                                label: "User-Intent Display",
                                info: "Show each session's task summary — the agent's one-line summary, or its first user prompt — as a purple block beneath the session, mirroring the orange \"waiting\" question block.",
                                beta: true
                            )

                            // Backchannel (issue #724): control discovered agents via
                            // their terminal backend. Default OFF; the daemon is the
                            // source of truth (mirrors the task-eta reconcile pattern).
                            // The rule editor appears only while this is on.
                            LeadingToggle(
                                isOn: $backchannelActivation,
                                label: "Backchannel (control sessions)",
                                info: "Let Irrlicht send input/interrupts into a running session via its terminal backend (tmux, kitty, …), and run event→action rules. Off by default; each agent still needs the \"control\" permission granted.",
                                beta: true
                            )
                            .onChange(of: backchannelActivation) { newValue in
                                if backchannelReconciling { backchannelReconciling = false; return }
                                Task {
                                    if let actual = await BackchannelActivationClient.set(enabled: newValue), actual != newValue {
                                        backchannelReconciling = true
                                        backchannelActivation = actual
                                    }
                                }
                            }
                            .onAppear {
                                Task {
                                    if let actual = await BackchannelActivationClient.status(), actual != backchannelActivation {
                                        backchannelReconciling = true
                                        backchannelActivation = actual
                                    }
                                }
                            }

                            if backchannelActivation {
                                BackchannelRulesView()
                                    .padding(.leading, IrrSpacing.sp4)
                            }

                            VStack(alignment: .leading, spacing: IrrSpacing.sp2) {
                                HStack(spacing: 6) {
                                    Text("Sources")
                                        .font(.subheadline)
                                        .fontWeight(.medium)
                                    BetaBadge()
                                    Spacer()
                                }

                                LeadingToggle(
                                    isOn: $useLocalDaemon,
                                    label: "Local",
                                    info: "Watch the daemon this app connects to directly."
                                )

                                LeadingToggle(
                                    isOn: $useRelayServer,
                                    label: "Relay server",
                                    info: "Also connect to a relay to see sessions from other machines."
                                )

                                LeadingToggle(
                                    isOn: $publishToRelay,
                                    label: "Publish to relay",
                                    info: "Forward this Mac's sessions to the relay so machines signed in with a token from the same account can see them. Keeps serving locally too."
                                )

                                // URL + token are shared by both directions: show
                                // them whenever either subscribe or publish is on.
                                if useRelayServer || publishToRelay {
                                    HStack(spacing: 6) {
                                        TextField(Self.defaultRelayURLPlaceholder, text: $relayURLDraft)
                                            .textFieldStyle(.roundedBorder)
                                            .font(.system(.caption, design: .monospaced))
                                            .autocorrectionDisabled(true)
                                            .onChange(of: relayURLDraft) { newValue in
                                                relayURLDebounceTask?.cancel()
                                                relayURLDebounceTask = Task {
                                                    try? await Task.sleep(nanoseconds: 600_000_000)
                                                    guard !Task.isCancelled else { return }
                                                    relayServerURL = newValue.trimmingCharacters(in: .whitespacesAndNewlines)
                                                    // POST the new URL to the running daemon so it
                                                    // reconfigures its forwarder live (issue #722).
                                                    daemonManager.publishSettingsDidChange()
                                                }
                                            }
                                        if useRelayServer {
                                            RelaySubscribeStatusDot(sessionManager: sessionManager)
                                        }
                                    }
                                    SecureField("Relay token (leave empty if the relay has no auth)", text: $relayTokenDraft)
                                        .textFieldStyle(.roundedBorder)
                                        .font(.system(.caption, design: .monospaced))
                                        .autocorrectionDisabled(true)
                                        .onChange(of: relayTokenDraft) { newValue in
                                            KeychainStore.set(newValue.trimmingCharacters(in: .whitespacesAndNewlines), account: "relayToken")
                                            // Keychain writes don't fire UserDefaults.didChangeNotification,
                                            // so nudge both directions to pick up the new token: the
                                            // subscribe link reconnects, and we POST the new token to the
                                            // daemon so it republishes with it (issue #722).
                                            sessionManager.relayTokenDidChange()
                                            daemonManager.publishSettingsDidChange()
                                        }

                                    if publishToRelay {
                                        HStack(spacing: 6) {
                                            Circle()
                                                .fill(publishStatus.state.dotColor)
                                                .frame(width: 8, height: 8)
                                            Text("Publishing — \(publishStatus.state.label)")
                                                .font(.caption)
                                                .foregroundColor(.secondary)
                                            Spacer()
                                        }
                                    }

                                    Text("Publish and subscribe must use tokens from the same account to see each other's sessions.")
                                        .font(.caption)
                                        .foregroundColor(.secondary)
                                }
                            }
                            .onAppear {
                                relayURLDraft = relayServerURL
                                relayTokenDraft = KeychainStore.get(account: "relayToken")
                                if publishToRelay { publishStatus.start() }
                            }
                            .onDisappear {
                                publishStatus.stop()
                            }
                            .onChange(of: useRelayServer) { on in
                                if on && relayURLDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                                    relayURLDraft = Self.defaultRelayURLPlaceholder
                                }
                                commitRelayURL()
                            }
                            .onChange(of: publishToRelay) { on in
                                if on && relayURLDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                                    relayURLDraft = Self.defaultRelayURLPlaceholder
                                }
                                commitRelayURL()
                                daemonManager.publishSettingsDidChange()
                                if on { publishStatus.start() } else { publishStatus.stop() }
                            }

                            CLIToolSection()
                        }
                    }
                }
                .padding(.horizontal, IrrSpacing.sp4)
                .padding(.vertical, IrrSpacing.sp4)
            }
            .frame(maxHeight: 520)
            .fixedSize(horizontal: false, vertical: true)

            Divider()

            // "Back" in the header above is the only way to close this panel
            // now (issue #940 — no separate footer "Done" button), so the
            // footer is just the version string, matching History's footer-less
            // pattern as closely as Settings' still-useful version display allows.
            HStack {
                Text("Irrlicht v\(appVersion)")
                    .font(.caption)
                    .foregroundColor(.secondary)
                Spacer()
            }
            .padding(.horizontal, IrrSpacing.sp4)
            .padding(.top, IrrSpacing.sp3)
            .padding(.bottom, IrrSpacing.sp4)
        }
        .frame(width: SessionListView.panelWidth)
        .background(Color(NSColor.windowBackgroundColor))
        .toggleStyle(IrrlichtSwitchToggleStyle())
        .background(
            WindowAccessor { window in
                // The panel is .borderless + .nonactivatingPanel + orderFrontRegardless(),
                // so canBecomeKey is false by default and the app is never active.
                // Activate + makeKey here so Settings text fields are editable.
                NSApp.activate(ignoringOtherApps: true)
                window.makeKey()
            }
        )
    }

    private var lastCheckedDescription: String {
        guard let date = updateManager.lastUpdateCheckDate else { return "Never" }
        let formatter = RelativeDateTimeFormatter()
        formatter.unitsStyle = .short
        return formatter.localizedString(for: date, relativeTo: Date())
    }

    @ViewBuilder
    private func providerModeRow(label: String, selection: Binding<String>) -> some View {
        HStack {
            Text(label)
                .font(.callout)
                .frame(width: 80, alignment: .leading)
            // A .menu (not .segmented) picker: the "Subscription" option makes a
            // segmented control ~315pt wide, which overflows the panel and
            // clips the whole settings stack. The dropdown stays compact.
            Picker("", selection: selection) {
                ForEach(ProviderModePreference.allCases) { mode in
                    Text(mode.label).tag(mode.rawValue)
                }
            }
            .pickerStyle(.menu)
            .labelsHidden()
            .fixedSize()
            Spacer()
        }
    }

    /// Provider keys with a rate_limit-carrying session right now, for the
    /// Usage/Combined quota-provider picker. Not persisted anywhere else —
    /// recomputed from whatever sessions happen to be visible when Settings
    /// is open. Excludes Gas Town-owned sessions to match exactly what
    /// MenuBarImageBuilder.combinedImage feeds into QuotaMenuBarRenderer —
    /// offering a provider here that the icon can never actually render
    /// (because its only carrying sessions are Gas Town's) would leave the
    /// picker selection pointing at a permanently-empty quota display.
    private var knownQuotaProviderKeys: [String] {
        let gasTownProvider = sessionManager.gasTownProvider
        let eligibleSessions = gasTownProvider?.isDaemonRunning == true
            ? sessionManager.sessions.filter { !(gasTownProvider?.ownsSession($0) ?? false) }
            : sessionManager.sessions
        var keys = Set<String>()
        for session in eligibleSessions {
            guard let snap = session.metrics?.rateLimit, !snap.windows.isEmpty else { continue }
            keys.insert(snap.providerKey(adapter: session.adapter) ?? "unknown:\(session.adapter ?? "")")
        }
        return keys.sorted()
    }

    /// `providerKey(adapter:)` returns nil for plan types/adapters it
    /// doesn't recognize (e.g. Team/Enterprise plans, or a wrapper adapter
    /// like Pi/OpenCode whose inherited snapshot carries no plan type) —
    /// `knownQuotaProviderKeys` then falls back to a raw `"unknown:<adapter>"`
    /// bucket key. Surface the adapter name instead of that internal-looking
    /// string so the picker still reads as a plausible option rather than
    /// leaking implementation detail.
    private func quotaProviderLabel(_ key: String) -> String {
        switch key {
        case "anthropic": return "Claude"
        case "openai": return "Codex"
        default:
            guard key.hasPrefix("unknown:") else { return key }
            let adapter = key.dropFirst("unknown:".count)
            return adapter.isEmpty ? "Other" : adapter.capitalized
        }
    }

    private func commitRelayURL() {
        relayServerURL = relayURLDraft.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// Reflect the login item's real system status (not just the stored
    /// preference) so the `.requiresApproval` hint can appear. An optional
    /// delay lets a detached register/unregister land first.
    private func refreshLoginItemStatus(after delay: TimeInterval = 0) {
        DispatchQueue.main.asyncAfter(deadline: .now() + delay) {
            loginItemStatus = LoginItemManager.status
        }
    }

    /// One-time migration for the new master "Enable notifications" toggle
    /// (issue #940): a brand-new `@AppStorage` key defaults to `false`, which
    /// would hide an existing user's already-configured per-event toggles
    /// behind a collapsed, seemingly-off section. Runs only while the key is
    /// still absent from `UserDefaults` — once set (by this reconcile or by
    /// the user), it's never overridden again.
    private func reconcileNotificationsMasterDefault() {
        guard UserDefaults.standard.object(forKey: "notificationsEnabled") == nil else { return }
        notificationsEnabled = notifyOnReady || notifyOnWaiting || notifyOnContextPressure
    }

    private func checkNotificationAuth() {
        guard Bundle.main.bundleIdentifier != nil,
              Bundle.main.bundleURL.pathExtension == "app" else { return }
        let center = UNUserNotificationCenter.current()
        center.getNotificationSettings { settings in
            DispatchQueue.main.async {
                notificationsDenied = settings.authorizationStatus == .denied
            }
            let anyEnabled = NotificationEvent.allCases.contains {
                UserDefaults.standard.bool(forKey: $0.enabledKey)
            }
            if settings.authorizationStatus == .notDetermined, anyEnabled {
                DispatchQueue.main.async {
                    center.requestAuthorization(options: [.alert, .sound]) { granted, _ in
                        DispatchQueue.main.async {
                            notificationsDenied = !granted
                        }
                    }
                }
            }
        }
    }
}

/// Live relay-subscribe connection dot. Isolated into its own @ObservedObject
/// view so it stays reactive while the parent SettingsView holds SessionManager
/// only as a non-observing reference — only this 8pt dot repaints per push, not
/// the whole settings form (issue #729). Rendered only when Advanced → Sources
/// is expanded and "Relay server" is on.
private struct RelaySubscribeStatusDot: View {
    @ObservedObject var sessionManager: SessionManager

    var body: some View {
        Circle()
            .fill(sessionManager.relayConnectionState.dotColor)
            .frame(width: 8, height: 8)
            .tooltip("Subscribe: \(sessionManager.relayConnectionState.shortLabel)")
    }
}

/// Settings subsection for putting the bundle-embedded irrlicht-ls CLI on
/// PATH (#608). Consent-first: the link is only created when the user clicks
/// the button — installers handle the automatic path; this covers DMG
/// drag-installs. Hidden in dev builds (binary not embedded).
private struct CLIToolSection: View {
    @State private var status: CLIToolInstaller.Status = .unavailable
    @State private var errorMessage: String?

    var body: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp3) {
            Text("Command-Line Tool")
                .font(.subheadline)
                .fontWeight(.medium)

            switch status {
            case .unavailable:
                Text("irrlicht-ls is not embedded in this build.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            case .installed(let path):
                HStack(spacing: 4) {
                    Image(systemName: "checkmark.circle.fill")
                        .foregroundColor(.green)
                        .font(.caption)
                    // Middle-truncate so a long install path stays on one line
                    // instead of forcing the whole Settings panel wider than its
                    // 360pt frame (which centers + clips every row).
                    Text("irrlicht-ls installed at \(path)")
                        .font(.caption)
                        .foregroundColor(.secondary)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            case .notInstalled:
                Text("Make the irrlicht-ls session list available in your terminal.")
                    .font(.caption)
                    .foregroundColor(.secondary)
                Button("Install Command-Line Tool") {
                    switch CLIToolInstaller.install() {
                    case .installed:
                        errorMessage = nil
                    case .failed(let message):
                        errorMessage = message
                    }
                    status = CLIToolInstaller.status()
                }
                .controlSize(.small)
                if let errorMessage {
                    Text(errorMessage)
                        .font(.caption)
                        .foregroundColor(.orange)
                        .textSelection(.enabled)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
        .onAppear { status = CLIToolInstaller.status() }
    }
}

private struct WindowAccessor: NSViewRepresentable {
    let onWindow: (NSWindow) -> Void
    func makeNSView(context _: Context) -> NSView {
        let v = NSView()
        // Defer one runloop so NSView.window is populated (nil during makeNSView).
        DispatchQueue.main.async { [weak v] in
            if let w = v?.window { onWindow(w) }
        }
        return v
    }
    func updateNSView(_: NSView, context _: Context) {
        // NSViewRepresentable requires this method; there's nothing to
        // update — onWindow already fired from makeNSView once the view
        // had a window.
    }
}

/// The "Alert at [value] [% / tokens]" control under the context-pressure
/// notification row. One threshold, entered as a percentage of the context
/// window or as an absolute token count (issue #689).
private struct ContextThresholdRow: View {
    @AppStorage(ContextPressureThreshold.valueKey) private var value: Double = ContextPressureThreshold.defaultValue
    @AppStorage(ContextPressureThreshold.unitKey) private var unitRaw: String = ContextPressureThreshold.defaultUnit.rawValue
    let enabled: Bool

    @State private var draft: String = ""
    @FocusState private var fieldFocused: Bool

    private var unit: ContextPressureThreshold.Unit {
        ContextPressureThreshold.Unit(rawValue: unitRaw) ?? ContextPressureThreshold.defaultUnit
    }

    var body: some View {
        HStack(spacing: IrrSpacing.sp2) {
            Text("Alert at")
                .font(.caption)
                .foregroundColor(.secondary)

            TextField("", text: $draft)
                .textFieldStyle(.roundedBorder)
                .multilineTextAlignment(.trailing)
                .font(.system(.caption, design: .monospaced))
                .frame(width: 72)
                .focused($fieldFocused)
                .onSubmit { commit() }
                .onChange(of: fieldFocused) { focused in
                    if !focused { commit() }
                }
                .tooltip(unit == .percent
                    ? "Alert when context reaches this % of the window"
                    : "Alert when the session reaches this many tokens")

            Picker("", selection: $unitRaw) {
                Text("%").tag(ContextPressureThreshold.Unit.percent.rawValue)
                Text("tokens").tag(ContextPressureThreshold.Unit.tokens.rawValue)
            }
            .labelsHidden()
            .frame(width: 92)
            .onChange(of: unitRaw) { newRaw in
                let newUnit = ContextPressureThreshold.Unit(rawValue: newRaw) ?? ContextPressureThreshold.defaultUnit
                value = ContextPressureThreshold.defaultValue(for: newUnit)
                draft = formatted(value)
            }

            Spacer()
        }
        .disabled(!enabled)
        .onAppear { draft = formatted(value) }
    }

    private func formatted(_ v: Double) -> String { "\(Int(v))" }

    /// Parse, clamp, and persist the draft, then reformat the field from the
    /// stored value so it never displays a number that wasn't saved (an empty
    /// or unparseable entry reverts to the current value).
    private func commit() {
        let trimmed = draft.trimmingCharacters(in: .whitespaces)
        if let parsed = Double(trimmed) {
            switch unit {
            case .percent: value = min(max(parsed, 1), 99)
            case .tokens: value = max(parsed, 1)
            }
        }
        draft = formatted(value)
    }
}

/// One row in the Settings notifications section: enable toggle, sound
/// picker, and a ▶ preview button.
private struct NotificationEventRow: View {
    let event: NotificationEvent
    @Binding var enabled: Bool
    let sampleText: String
    let onImportError: (String) -> Void

    @State private var selection: SoundChoice = .default

    var body: some View {
        // One row (issue #940): toggle + label + sound picker + preview all
        // share a line — LeadingToggle's trailing Spacer absorbs the gap
        // between the label and the fixed-width picker/button, so this
        // stays a single line regardless of how long `event.displayName` is.
        VStack(alignment: .leading, spacing: IrrSpacing.sp1) {
            HStack(spacing: IrrSpacing.sp2) {
                LeadingToggle(isOn: $enabled, label: event.displayName)

                Picker("", selection: $selection) {
                    ForEach(SoundChoice.builtIns, id: \.self) { choice in
                        Text(choice.displayName).tag(choice)
                    }
                    Divider()
                    Text("None").tag(SoundChoice.none)
                    ForEach(SoundChoice.speakChoices, id: \.self) { choice in
                        Text(choice.displayName).tag(choice)
                    }
                    if case .custom = selection {
                        Text(selection.displayName).tag(selection)
                    }
                    Divider()
                    Text("Custom audio file…").tag(SoundChoice.customPickerSentinel)
                }
                .labelsHidden()
                .disabled(!enabled)
                .frame(width: 112)
                .tooltip(selection.displayName)
                .onChange(of: selection) { newValue in
                    handle(newValue)
                }

                Button {
                    SoundPlayer.preview(selection, sampleText: sampleText)
                } label: {
                    Image(systemName: "play.fill")
                        .frame(width: 10, height: 10)
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
                .disabled(!enabled || selection == .none)
                .tooltip("Preview")
            }

            // A premium voice (Jamie/Zoe) may not be installed; offer a path to
            // System Settings. We deliberately do NOT probe install state via
            // AVSpeechSynthesisVoice.speechVoices() — that boots the TextToSpeech
            // + accessibility subsystem and crashes (SIGBUS) on macOS 26.x when
            // run off the main thread (#780). The link is unconditional instead.
            if case .speak(let voice) = selection, voice != .default {
                Button {
                    Self.openSpokenContentSettings()
                } label: {
                    HStack(spacing: 4) {
                        Image(systemName: "arrow.down.circle")
                        Text("Manage \(voice.displayName) in System Settings")
                    }
                    .font(.caption2)
                }
                .buttonStyle(.link)
                .tooltip("Open System Settings → Accessibility → Spoken Content")
            }
        }
        .onAppear { loadFromDefaults() }
    }

    private static func openSpokenContentSettings() {
        let candidates = [
            "x-apple.systempreferences:com.apple.Accessibility-Settings.extension?SpokenContent",
            "x-apple.systempreferences:com.apple.preference.universalaccess",
        ]
        for raw in candidates {
            if let url = URL(string: raw), NSWorkspace.shared.open(url) { return }
        }
    }

    private func loadFromDefaults() {
        let raw = UserDefaults.standard.string(forKey: event.soundKey) ?? event.defaultSound.rawValue
        selection = SoundChoice(rawValue: raw) ?? event.defaultSound
    }

    private func handle(_ newValue: SoundChoice) {
        if newValue == .customPickerSentinel {
            let previous = SoundChoice(rawValue: UserDefaults.standard.string(forKey: event.soundKey) ?? "") ?? .default
            selection = previous
            pickCustomFile()
            return
        }
        UserDefaults.standard.set(newValue.rawValue, forKey: event.soundKey)
    }

    private func pickCustomFile() {
        let panel = NSOpenPanel()
        panel.allowsMultipleSelection = false
        panel.canChooseDirectories = false
        panel.canChooseFiles = true
        panel.allowedContentTypes = [UTType.audio]
        panel.message = "Choose an audio file (aiff, wav, mp3, m4a, caf)"
        let response = panel.runModal()
        guard response == .OK, let url = panel.url else { return }

        SoundPlayer.installCustom(srcURL: url, event: event) { result in
            switch result {
            case .success(let installed):
                let choice = SoundChoice.custom(installedFilename: installed, displayPath: url.path)
                UserDefaults.standard.set(choice.rawValue, forKey: event.soundKey)
                selection = choice
                onImportError("")
            case .failure(let error):
                onImportError("Could not import audio file: \(error.localizedDescription)")
            }
        }
    }
}

/// Left-aligned switch + label, rendered by `IrrlichtSwitchToggleStyle`.
/// An optional `info` string adds a hover-reveal ⓘ next to the label so the
/// explanation doesn't cost a permanent block of caption text below the row.
/// Internal (not private): PermissionWizardView reuses it.
struct LeadingToggle: View {
    @Binding var isOn: Bool
    let label: String
    var info: String? = nil
    /// Flags a still-maturing feature with a "BETA" pill after the label (#694).
    var beta: Bool = false

    var body: some View {
        HStack(spacing: 6) {
            Toggle(isOn: $isOn) { Text(label) }
                .fixedSize()
            if let info {
                InfoIcon(text: info)
            }
            if beta {
                BetaBadge()
            }
            Spacer()
        }
    }
}

/// Small "BETA" pill flagging a feature that's still maturing (#694). Mirrors
/// the web dashboard's `.beta-badge`.
struct BetaBadge: View {
    var body: some View {
        Text("BETA")
            .font(.system(size: 9, weight: .bold))
            .tracking(0.5)
            .foregroundColor(IrrColors.working)
            .padding(.horizontal, 5)
            .padding(.vertical, 1)
            .background(Capsule().fill(IrrColors.workingDim))
            .accessibilityLabel("Beta feature")
    }
}

/// Small ⓘ affordance: reveals a one-line explainer on hover instead of
/// spending vertical space on a caption paragraph under every control.
/// Internal (not private): PermissionWizardView reuses it.
struct InfoIcon: View {
    let text: String

    var body: some View {
        Image(systemName: "info.circle")
            .font(.caption)
            .foregroundColor(.secondary)
            .tooltip(text)
            .accessibilityLabel(text)
    }
}

/// Custom switch style: a green-on / neutral-off capsule with a sliding
/// white knob. Replaces `ToggleStyle.switch` because macOS's NSSwitch-backed
/// switch ignores `.tint(_:)` — its on color is locked to the system accent.
/// Drawing the pill ourselves makes the color independent of System Settings.
/// Internal (not private): PermissionWizardView applies it too.
struct IrrlichtSwitchToggleStyle: ToggleStyle {
    func makeBody(configuration: Configuration) -> some View {
        HStack(spacing: IrrSpacing.sp2) {
            ZStack(alignment: configuration.isOn ? .trailing : .leading) {
                Capsule()
                    // Off state needs its own visible fill + stroke, not just a
                    // dim tint of the on-color — at 0.4 opacity the old fill
                    // read as almost invisible against the dark background,
                    // leaving off switches looking like a bare floating knob
                    // rather than a switch (issue #940).
                    .fill(configuration.isOn ? IrrColors.ready : Color.primary.opacity(0.14))
                    .overlay(
                        Capsule().strokeBorder(Color.primary.opacity(configuration.isOn ? 0 : 0.20), lineWidth: 1)
                    )
                Circle()
                    .fill(Color.white)
                    .shadow(color: Color.black.opacity(0.18), radius: 1, x: 0, y: 0.5)
                    .padding(2)
            }
            .frame(width: 30, height: 18)
            .animation(IrrMotion.easeOut(duration: IrrMotion.fast), value: configuration.isOn)

            configuration.label
        }
        .contentShape(Rectangle())
        .onTapGesture { configuration.isOn.toggle() }
        .accessibilityAddTraits(.isButton)
        .accessibilityValue(configuration.isOn ? "on" : "off")
    }
}

private extension SoundChoice {
    static let customPickerSentinel: SoundChoice = .custom(installedFilename: "__picker_sentinel__", displayPath: "")
}
