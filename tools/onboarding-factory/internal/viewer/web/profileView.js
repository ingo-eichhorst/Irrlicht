// Execution-profile view for the onboarding viewer (#1889).
//
// Claude Code CLI Local and Claude Desktop Local both produce claudecode
// transcripts, so the viewer keeps their evidence apart: one profile's status,
// versions, recording link and raw evidence never stand in for the other's.
// Everything here is a pure function over one /api/scenarios detail payload
// (already scoped to a single profile by the server), so switching the profile
// is exactly "fetch the other payload and re-render".
//
// The module is separate from viewer.js — like playbackView.js and
// playbackTimeline.js — so it can be unit-tested without running viewer.js's
// top-level bootstrap.

export const CLI_PROFILE = "cli-local";
export const DESKTOP_PROFILE = "desktop-local";

// DEFAULT_PROFILE is the backward-compatible view. A viewer URL with no
// ?profile= keeps rendering Claude Code CLI Local evidence, which is what
// every pre-#1889 link means.
export const DEFAULT_PROFILE = CLI_PROFILE;

export const PROFILE_IDS = [CLI_PROFILE, DESKTOP_PROFILE];

const PROFILE_LABELS = {
  [CLI_PROFILE]: "Claude Code CLI Local",
  [DESKTOP_PROFILE]: "Claude Desktop Local",
};

// The three versions a Desktop Local observation pins, in display order.
const VERSION_ROWS = [
  ["desktop_app", "Claude Desktop app"],
  ["agent_cli", "bundled Claude Code"],
  ["irrlicht", "Irrlicht"],
];

// Outcome vocabulary of internal/desktopresults. Rendering an unknown outcome
// as a neutral chip (rather than silently as a pass) keeps a schema drift
// visible instead of flattering it.
const OUTCOME_KINDS = {
  "observed-passing": "pass",
  "observed-failure": "fail",
  "not-applicable": "neutral",
  unobservable: "neutral",
  "not-runnable": "neutral",
};

export function profileLabel(id) {
  return PROFILE_LABELS[id] || id || "";
}

// shouldRenderProfilePanel keeps every adapter that has no second profile —
// which today is every adapter but Claude Code, and every Claude Code cell
// with no Desktop evidence yet — rendering exactly the page it rendered before
// execution profiles existed. A one-option selector is not a choice, and the
// issue's "keep all non-Claude adapters unchanged" is easiest to hold by
// simply not drawing the control there.
export function shouldRenderProfilePanel(detail) {
  const d = detail || {};
  // A page reached under an explicit non-default profile ALWAYS says which
  // profile it is. The profile is a shareable URL parameter, so a Desktop
  // request that lands on an empty page must not be mistakable for the CLI
  // page — that is a live mis-read, not a corner case.
  if ((d.execution_profile || DEFAULT_PROFILE) !== DEFAULT_PROFILE) return true;
  if (d.desktop_result) return true;
  if (d.recordings_error) return true;
  return (d.profiles || []).filter(p => p.selectable).length > 1;
}

// profileFromHash reads ?profile= out of a viewer hash. An absent or
// unrecognised value is the CLI Local default — the viewer must never guess
// its way into showing the other profile's evidence.
export function profileFromHash(hash) {
  const q = String(hash || "").indexOf("?");
  if (q < 0) return DEFAULT_PROFILE;
  const value = new URLSearchParams(String(hash).slice(q + 1)).get("profile");
  return PROFILE_IDS.includes(value) ? value : DEFAULT_PROFILE;
}

// focusFromHash reads the pipeline strip's ?focus=<key> out of a viewer hash,
// alongside (in any order with) ?profile=.
export function focusFromHash(hash) {
  const q = String(hash || "").indexOf("?");
  if (q < 0) return "";
  const value = new URLSearchParams(String(hash).slice(q + 1)).get("focus") || "";
  return /^[a-z]+$/.test(value) ? value : "";
}

// recordingHash builds the viewer's STABLE deep link for one cell under one
// execution profile:
//
//   #/recording/<agent>/<subtree>/<id>[/<recording>][?profile=<p>[&focus=<k>]]
//
// The CLI Local default is omitted from the query, so every link written
// before profiles existed still names the same page. This is the link target
// the public support page points at.
export function recordingHash({agent, subtree, id, recording = "", profile = DEFAULT_PROFILE, focus = ""} = {}) {
  const parts = [agent, subtree, id].map(encodeURIComponent).join("/");
  let hash = `#/recording/${parts}`;
  if (recording) hash += `/${encodeURIComponent(recording)}`;
  const query = new URLSearchParams();
  if (profile && profile !== DEFAULT_PROFILE) query.set("profile", profile);
  if (focus) query.set("focus", focus);
  const suffix = query.toString();
  return suffix ? `${hash}?${suffix}` : hash;
}

// profileStatus reduces one detail payload to the single status the profile
// selector's panel shows. The four "nothing to show" cases are deliberately
// distinct strings: "no result recorded", "evidence rejected", "no recording"
// and a real pass/fail must never render as the same thing.
export function profileStatus(detail) {
  const d = detail || {};
  if ((d.execution_profile || DEFAULT_PROFILE) === DESKTOP_PROFILE) {
    return desktopStatus(d);
  }
  return recordedStatus(d);
}

function desktopStatus(detail) {
  const result = detail.desktop_result;
  if (result) {
    if (result.error) {
      return {label: "evidence rejected", kind: "error", detail: result.error};
    }
    return {
      label: result.outcome || "unknown",
      kind: OUTCOME_KINDS[result.outcome] || "neutral",
      detail: result.reason || "",
    };
  }
  // No explicit result. A Desktop recording still has a validated outcome the
  // server already computed, and reporting "no Desktop result" over the top of
  // it would throw away real evidence — so fall through to it, and only say
  // there is no result when there is genuinely nothing.
  if (detail.latest_recording || detail.recordings_error) {
    return recordedStatus(detail);
  }
  return {
    label: "no Desktop result",
    kind: "none",
    detail: "This cell carries no execution-results.json and no Claude Desktop Local recording, so no Desktop answer has been recorded for it.",
  };
}

function recordedStatus(detail) {
  // An unreadable recording history is a finding, not an absence. It outranks
  // everything below: without it "we could not look" renders as "nothing here".
  if (detail.recordings_error) {
    return {label: "history unreadable", kind: "error", detail: detail.recordings_error};
  }
  if (!detail.latest_recording) {
    return {
      label: "no recording",
      kind: "none",
      detail: "No recording has been captured for this cell under this execution profile.",
    };
  }
  if (!detail.expected) {
    return {label: "recorded", kind: "neutral", detail: "No expected.jsonl to validate this recording against."};
  }
  return {
    label: detail.expected.pass ? "pass" : "fail",
    kind: detail.expected.pass ? "pass" : "fail",
    detail: detail.expected.summary || "",
  };
}

// profileVersions returns the three versions for this profile's evidence.
//
// Where a Desktop result exists it GOVERNS, even when it carries no versions:
// the server fills them in only from a recording it confirmed is
// desktop-local, so falling back to the cell's newest manifest here would
// re-attach versions to a result the server had just refused. Every other view
// reads the profile-scoped latest manifest.
export function profileVersions(detail) {
  const d = detail || {};
  if (d.execution_profile === DESKTOP_PROFILE && d.desktop_result) {
    const v = d.desktop_result.versions || {};
    return versionTriple(v.desktop_app, v.agent_cli, v.irrlicht);
  }
  const m = d.latest_manifest || {};
  return versionTriple(m.desktop_app_version, m.agent_cli_version, m.daemon_version);
}

function versionTriple(desktopApp, agentCli, irrlicht) {
  return {desktop_app: desktopApp || "", agent_cli: agentCli || "", irrlicht: irrlicht || ""};
}

// profileRecordingLink returns {name, hash} for the recording this profile's
// evidence rests on, or null when there is none.
//
// Under Desktop Local the RESULT governs: the server clears
// desktop_result.recording whenever the named recording is not desktop-local,
// so a refused result yields no link at all rather than a link to CLI bytes.
export function profileRecordingLink(detail) {
  const d = detail || {};
  const profile = d.execution_profile || DEFAULT_PROFILE;
  const result = profile === DESKTOP_PROFILE ? d.desktop_result : null;
  const name = result ? result.recording : d.latest_recording;
  if (!name) return null;
  return {
    name,
    hash: recordingHash({agent: d.agent, subtree: d.subtree, id: d.id, recording: name, profile}),
  };
}

// evidenceHref is the API path serving one raw Desktop identity-evidence file
// out of the linked recording.
export function evidenceHref(detail, recording, file) {
  const d = detail || {};
  const parts = [d.agent, d.subtree, d.id].map(encodeURIComponent).join("/");
  return `/api/scenarios/${parts}/recordings/${encodeURIComponent(recording)}` +
    `/evidence/${encodeURIComponent(file)}?profile=${encodeURIComponent(DESKTOP_PROFILE)}`;
}

// profileOptionSuffix describes one option's evidence. A profile whose history
// could not be read says so instead of reporting "0 recordings", which would
// be the same label a genuinely empty profile gets.
function profileOptionSuffix(option) {
  if (option.error) return "history unreadable";
  const count = `${option.recordings} recording${option.recordings === 1 ? "" : "s"}`;
  return option.has_result ? `${count}, explicit result` : count;
}

// buildProfileSelector renders the profile <select>. Only profiles the server
// marked selectable are offered, and each option says how many recordings that
// profile actually has, so "Desktop Local (0 recordings)" is legible rather
// than looking like a missing page.
export function buildProfileSelector(detail, onChange) {
  const d = detail || {};
  const current = d.execution_profile || DEFAULT_PROFILE;
  const wrap = document.createElement("div");
  wrap.dataset.testid = "profile-selector";
  const label = document.createElement("b");
  label.textContent = "Execution profile: ";
  const select = document.createElement("select");
  select.dataset.testid = "profile-select";
  select.style.cssText = "padding: 4px 8px; font: inherit; font-size: 12px; border: 1px solid #c0bdb1; border-radius: 3px;";
  for (const option of (d.profiles || []).filter(p => p.selectable)) {
    const el = document.createElement("option");
    el.value = option.id;
    el.textContent = `${profileLabel(option.id)} — ${profileOptionSuffix(option)}`;
    select.appendChild(el);
  }
  select.value = current;
  if (typeof onChange === "function") {
    select.addEventListener("change", () => onChange(select.value));
  }
  wrap.append(label, select);
  return wrap;
}

// renderProfileEvidence builds the whole per-profile evidence body: status,
// versions, recording link, raw identity evidence, and — for a non-observed
// Desktop outcome — the evidence-based reason that IS the answer.
export function renderProfileEvidence(detail) {
  const d = detail || {};
  const box = document.createElement("div");
  box.dataset.testid = "profile-evidence";
  box.dataset.profile = d.execution_profile || DEFAULT_PROFILE;
  box.append(statusRow(profileStatus(d)));
  box.append(versionRows(profileVersions(d)));
  box.append(recordingRow(d));
  const result = d.execution_profile === DESKTOP_PROFILE ? d.desktop_result : null;
  if (result) {
    appendReasonRows(box, result);
    appendEvidenceRows(box, d, result);
  }
  return box;
}

const KIND_COLORS = {
  pass: ["#d6f0d4", "#1f5a1d"],
  fail: ["#f8c8c8", "#8a0000"],
  error: ["#f8c8c8", "#8a0000"],
  neutral: ["#eaeae0", "#555"],
  none: ["#eaeae0", "#777"],
};

function statusRow(status) {
  const row = document.createElement("div");
  row.dataset.testid = "profile-status";
  row.dataset.kind = status.kind;
  row.style.cssText = "margin-bottom: 8px;";
  const chip = document.createElement("span");
  chip.className = "badge";
  const [bg, fg] = KIND_COLORS[status.kind] || KIND_COLORS.neutral;
  chip.style.cssText = `background:${bg};color:${fg};`;
  chip.textContent = status.label;
  row.append(chip);
  if (status.detail) {
    const note = document.createElement("span");
    note.style.cssText = "margin-left: 8px; color: #555;";
    note.textContent = status.detail;
    row.append(note);
  }
  return row;
}

function versionRows(versions) {
  const table = document.createElement("table");
  table.dataset.testid = "profile-versions";
  for (const [key, label] of VERSION_ROWS) {
    const tr = document.createElement("tr");
    const th = document.createElement("td");
    th.style.cssText = "width: 180px; color: #666;";
    th.textContent = label;
    const td = document.createElement("td");
    td.dataset.version = key;
    const code = document.createElement("code");
    code.textContent = versions[key] || "—";
    td.append(code);
    tr.append(th, td);
    table.appendChild(tr);
  }
  return table;
}

function recordingRow(detail) {
  const row = document.createElement("div");
  row.dataset.testid = "profile-recording";
  row.style.cssText = "margin-top: 8px;";
  const link = profileRecordingLink(detail);
  const label = document.createElement("b");
  label.textContent = "Recording: ";
  row.append(label);
  if (!link) {
    const none = document.createElement("i");
    none.textContent = "none for this execution profile";
    row.append(none);
    return row;
  }
  const a = document.createElement("a");
  a.dataset.testid = "profile-recording-link";
  a.href = link.hash;
  a.textContent = link.name;
  row.append(a);
  return row;
}

function appendReasonRows(box, result) {
  for (const [testid, label, value] of [
    ["profile-reason", "Reason", result.reason],
    ["profile-missing-control", "Missing Desktop control", result.missing_control],
  ]) {
    if (!value) continue;
    const row = document.createElement("div");
    row.dataset.testid = testid;
    row.style.cssText = "margin-top: 6px;";
    const b = document.createElement("b");
    b.textContent = `${label}: `;
    row.append(b, value);
    box.append(row);
  }
  for (const ref of result.evidence_refs || []) {
    const row = document.createElement("div");
    row.dataset.testid = "profile-evidence-ref";
    row.style.cssText = "margin-top: 4px; font-size: 11px;";
    const code = document.createElement("code");
    code.textContent = ref;
    row.append(code);
    box.append(row);
  }
}

// appendEvidenceRows links the six raw identity-evidence files. They only
// exist for a result whose recording survived the server's profile check, so a
// refused result renders its error and no links at all.
function appendEvidenceRows(box, detail, result) {
  if (!result.recording || !(result.evidence || []).length) return;
  const list = document.createElement("div");
  list.dataset.testid = "profile-raw-evidence";
  list.style.cssText = "margin-top: 8px;";
  const label = document.createElement("b");
  label.textContent = "Raw identity evidence: ";
  list.append(label);
  for (const link of result.evidence) {
    list.append(evidenceNode(detail, result.recording, link));
  }
  box.append(list);
}

// evidenceNode renders one reference. The three outcomes are distinct on
// purpose: a link (present under its canonical name), a non-canonical
// reference (the bytes exist, but the contract's name was not used — a
// violation, not a missing file, and not servable through the allowlisted
// route), and a genuinely missing file.
function evidenceNode(detail, recording, link) {
  if (!link.present) {
    return evidenceNote("profile-evidence-missing", `${link.field} (missing)`, "#8a0000");
  }
  if (!link.canonical) {
    return evidenceNote("profile-evidence-noncanonical",
      `${link.field} (referenced as "${link.file}", not the contract name)`, "#8a4500");
  }
  const a = document.createElement("a");
  a.dataset.testid = "profile-evidence-link";
  a.style.cssText = "margin-right: 8px;";
  a.href = evidenceHref(detail, recording, link.field);
  a.textContent = link.field;
  return a;
}

function evidenceNote(testid, text, color) {
  const span = document.createElement("span");
  span.dataset.testid = testid;
  span.style.cssText = `margin-right: 8px; color: ${color};`;
  span.textContent = text;
  return span;
}
