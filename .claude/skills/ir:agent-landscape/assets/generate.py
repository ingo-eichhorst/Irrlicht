#!/usr/bin/env python3
"""Render landscape/index.html and landscape/compare/index.html from agent-data.json."""
import html
import json
import math
from datetime import date
from pathlib import Path

SKILL_DIR = Path("/Users/ingo/projects/irrlicht/.claude/skills/ir:agent-landscape")
DATA_FILE = SKILL_DIR / "references" / "agent-data.json"
PAGE_TMPL = SKILL_DIR / "assets" / "page-template.html"
COMPARE_TMPL = SKILL_DIR / "assets" / "compare-template.html"
SITE_DIR = Path("/Users/ingo/projects/irrlicht/site/landscape")
OUT_INDEX = SITE_DIR / "index.html"
OUT_COMPARE = SITE_DIR / "compare" / "index.html"

TODAY = date.today().isoformat()
NEW_BADGE_DAYS = 90


def parse_iso(s: str) -> date:
    y, m, d = s.split("-")
    return date(int(y), int(m), int(d))


def fmt_n(n):
    if n is None:
        return "—"
    return f"{n:,}"


def latest_snapshot_other_than_today(history, today):
    """Return the newest snapshot strictly older than today (i.e. not today's)."""
    for entry in history:
        if entry["date"] != today:
            return entry
    return None


def score(agent, today_date):
    """
    Popularity score = log10(stars).  We deliberately do NOT add a short-window
    trend bonus until we have a snapshot ≥ 30 days old; a 12-day delta is too
    noisy to rank against.
    """
    if agent.get("stars") is None:
        return 0.0
    base = math.log10((agent["stars"] or 0) + 1)
    prior = latest_snapshot_other_than_today(agent.get("stars_history") or [], TODAY)
    if prior and prior.get("stars") is not None and prior["stars"] > 0:
        days = (today_date - parse_iso(prior["date"])).days
        if days >= 30:
            gain = max(0, agent["stars"] - prior["stars"])
            trend = gain / days * 30
            base = 0.75 * base + 0.25 * math.log10(trend + 1)
    return base


EARLIEST_FIRST_SEEN = None  # set in main() so we don't tag every first-batch agent NEW


def is_new(agent, today_date) -> bool:
    fs = agent.get("first_seen")
    if not fs:
        return False
    # Only flag agents added in a LATER batch than the very first scan,
    # and only while they're still within the NEW window.
    if EARLIEST_FIRST_SEEN and fs == EARLIEST_FIRST_SEEN:
        return False
    try:
        return (today_date - parse_iso(fs)).days < NEW_BADGE_DAYS
    except ValueError:
        return False


def growth_cell(agent):
    """
    Honest growth cell.  We only trust the single prior snapshot the last skill
    run wrote (~12 days ago) until more history accumulates.  Hallucinated
    Jan/April-1 entries were removed from the JSON.
    """
    hist = agent.get("stars_history") or []
    current = agent.get("stars")
    if current is None or not hist:
        return '<span class="growth-na">—</span>', None
    prior = latest_snapshot_other_than_today(hist, TODAY)
    if not prior or prior.get("stars") is None:
        return '<span class="growth-na">—</span>', None
    delta = current - prior["stars"]
    pct = (delta / prior["stars"] * 100) if prior["stars"] else 0
    cls = "growth-up" if delta > 0 else "growth-down" if delta < 0 else "growth-flat"
    sign = "+" if delta > 0 else ""
    label = f'<span class="{cls}">{sign}{delta:,} <span class="growth-pct">({sign}{pct:.1f}%)</span></span>'
    # Human-readable age
    days = max(1, (parse_iso(TODAY) - parse_iso(prior["date"])).days)
    return label, f"since {prior['date']} ({days}d)"


def support_badge(s):
    return {
        "live": '<span class="badge badge-live">live</span>',
        "planned": '<span class="badge badge-planned">planned</span>',
    }.get(s, '<span class="badge badge-none">not tracked</span>')


def alt_metric_display(a):
    m = a.get("alternative_metrics") or {}
    if "funding_millions_usd" in m and m["funding_millions_usd"]:
        return f'${m["funding_millions_usd"]}M raised', m.get("source")
    if "estimated_users" in m and m["estimated_users"]:
        u = m["estimated_users"]
        try:
            n = int(str(u).rstrip("+"))
            if n >= 1_000_000:
                return f'{n/1_000_000:.1f}M users', m.get("source")
            if n >= 1_000:
                return f'{n/1_000:.0f}k users', m.get("source")
        except ValueError:
            pass
        return f'{u} users', m.get("source")
    if "acquisition_price_millions_usd" in m and m["acquisition_price_millions_usd"]:
        return f'${m["acquisition_price_millions_usd"]}M acquisition', m.get("source")
    return "—", m.get("source")


def anchor(a):
    url = a.get("website") or (f'https://github.com/{a["github_repo"]}' if a.get("github_repo") else "#")
    name = html.escape(a["name"])
    new = '<sup class="badge badge-new">NEW</sup>' if is_new(a, parse_iso(TODAY)) else ""
    return f'<a href="{html.escape(url)}" target="_blank" rel="noopener">{name}</a>{new}'


def main_table_rows(agents, today_date):
    rows = []
    ranked = [a for a in agents if a.get("stars") is not None]
    ranked.sort(key=lambda a: score(a, today_date), reverse=True)
    for i, a in enumerate(ranked, 1):
        growth_html, _ = growth_cell(a)
        rows.append(f"""
  <tr>
    <td class="rank">#{i}</td>
    <td class="name">{anchor(a)}</td>
    <td class="stars">{fmt_n(a.get("stars"))}</td>
    <td class="growth growth-3m">{growth_html}</td>
    <td>{support_badge(a.get("irrlicht_support"))}</td>
    <td class="desc">{html.escape(a.get("description") or "")}</td>
  </tr>""")

    # No-repo group (unranked)
    nogit = [a for a in agents if a.get("stars") is None]
    nogit.sort(key=lambda x: x["name"].lower())
    if nogit:
        rows.append(f"""
  <tr><td colspan="6" style="padding-top:1.2rem; color: var(--text-dim); font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.08em; border-bottom: none;">No public repo — popularity via funding / users</td></tr>""")
        for a in nogit:
            metric, _src = alt_metric_display(a)
            rows.append(f"""
  <tr>
    <td class="rank">—</td>
    <td class="name">{anchor(a)}</td>
    <td class="alt-metric">{html.escape(metric)}</td>
    <td class="growth growth-3m"><span class="growth-na">—</span></td>
    <td>{support_badge(a.get("irrlicht_support"))}</td>
    <td class="desc">{html.escape(a.get("description") or "")}</td>
  </tr>""")

    return f"""<table class="landscape-table">
<thead><tr>
  <th class="rank">#</th><th>Name</th><th>Stars</th><th class="growth-3m">Recent growth</th><th>Irrlicht</th><th>Description</th>
</tr></thead>
<tbody>
{''.join(rows)}
</tbody></table>"""


def render_main():
    data = json.loads(DATA_FILE.read_text())
    today_date = parse_iso(TODAY)
    agents = [a for a in data["agents"] if a["category"] == "agent"]
    orchs = [a for a in data["agents"] if a["category"] == "orchestrator"]
    tmpl = PAGE_TMPL.read_text()

    prior_snapshot_dates = sorted({
        e["date"] for a in data["agents"] for e in (a.get("stars_history") or [])
        if e["date"] != TODAY
    })
    if prior_snapshot_dates:
        earliest = prior_snapshot_dates[0]
        days = (today_date - parse_iso(earliest)).days
        trend_note = (
            f"Recent growth = change in GitHub stars since the previous scan on "
            f"{earliest} ({days}d ago). 1-month and 3-month deltas will appear here "
            f"once snapshots are available at those horizons."
        )
    else:
        trend_note = "No prior snapshots yet — growth will appear after the next scan."

    agents_table = main_table_rows(agents, today_date)
    orch_table = main_table_rows(orchs, today_date)
    out = (tmpl
           .replace("{{LAST_UPDATED}}", data["last_updated"])
           .replace(
               '<p class="trend-note">Growth measured from snapshots closest to 1 month and 3 months before {{LAST_UPDATED}}.</p>',
               f'<p class="trend-note">{trend_note}</p>',
           )
           .replace("{{AGENTS_TABLE}}", agents_table)
           .replace("{{ORCHESTRATORS_TABLE}}", orch_table))
    OUT_INDEX.write_text(out)
    print(f"Wrote {OUT_INDEX} ({len(out):,} bytes)")
    return data


def compare_row(a, today_date):
    name_cell = f'<td class="name" data-sort="{html.escape(a["name"].lower())}">{anchor(a)}</td>'
    cat = a.get("category") or ""
    cat_badge = (
        '<span class="badge badge-cat badge-agent">Agent</span>'
        if cat == "agent"
        else '<span class="badge badge-cat badge-orchestrator">Orchestrator</span>'
    )
    stars = a.get("stars")
    if stars is not None:
        stars_cell = f'<td class="mono" data-sort="{stars}">{fmt_n(stars)}</td>'
    else:
        metric, _src = alt_metric_display(a)
        stars_cell = f'<td class="mono" data-sort="-1">{html.escape(metric)}</td>'

    growth_html, _ = growth_cell(a)
    growth_cell_html = f'<td class="growth-3m">{growth_html}</td>'

    oss = bool(a.get("github_repo"))
    oss_html = (
        '<span class="badge-oss-yes">Yes</span>' if oss else '<span class="badge-oss-no">No</span>'
    )

    license_ = a.get("license") or "—"
    lang = a.get("language") or "—"
    interface = a.get("interface") or "—"
    pricing = a.get("pricing") or "—"
    support = support_badge(a.get("irrlicht_support"))

    return f"""<tr>
  {name_cell}
  <td>{cat_badge}</td>
  {stars_cell}
  {growth_cell_html}
  <td data-sort="{1 if oss else 0}">{oss_html}</td>
  <td>{html.escape(license_)}</td>
  <td>{html.escape(lang)}</td>
  <td>{html.escape(interface)}</td>
  <td>{html.escape(pricing)}</td>
  <td>{support}</td>
</tr>"""


def render_compare():
    data = json.loads(DATA_FILE.read_text())
    today_date = parse_iso(TODAY)
    tmpl = COMPARE_TMPL.read_text()

    rows_ranked = sorted(
        [a for a in data["agents"] if a.get("stars") is not None],
        key=lambda a: score(a, today_date), reverse=True,
    )
    rows_unranked = sorted(
        [a for a in data["agents"] if a.get("stars") is None], key=lambda a: a["name"].lower()
    )

    all_rows = "\n".join(compare_row(a, today_date) for a in rows_ranked + rows_unranked)

    table_html = f"""<table class="compare-table">
<thead><tr>
<th>Name<span class="sort-arrow">▲</span></th>
<th>Category<span class="sort-arrow">▲</span></th>
<th>Stars / Metric<span class="sort-arrow">▲</span></th>
<th class="growth-3m">Recent growth<span class="sort-arrow">▲</span></th>
<th>Open Source<span class="sort-arrow">▲</span></th>
<th>License*<span class="sort-arrow">▲</span></th>
<th>Primary Language<span class="sort-arrow">▲</span></th>
<th>Interface<span class="sort-arrow">▲</span></th>
<th>Pricing*<span class="sort-arrow">▲</span></th>
<th>Irrlicht<span class="sort-arrow">▲</span></th>
</tr></thead>
<tbody>
{all_rows}
</tbody></table>
<p style="font-size:0.72rem; color: var(--text-dim); margin-top: 0.8rem; font-style: italic;">* License and pricing are sourced from the GitHub API and public web pages. Verify on the project's own website before relying on them. "Recent growth" is the absolute change in stars since the last scan snapshot ({{PRIOR_DATE}}); it is not a monthly or quarterly figure.</p>"""

    prior_dates = sorted({
        e["date"] for a in data["agents"] for e in (a.get("stars_history") or [])
        if e["date"] != TODAY
    })
    prior_label = prior_dates[0] if prior_dates else "—"
    table_html = table_html.replace("{{PRIOR_DATE}}", prior_label)

    out = (tmpl
           .replace("{{LAST_UPDATED}}", data["last_updated"])
           .replace("{{COMPARISON_TABLE}}", table_html))
    OUT_COMPARE.write_text(out)
    print(f"Wrote {OUT_COMPARE} ({len(out):,} bytes)")


def main():
    global EARLIEST_FIRST_SEEN
    data = json.loads(DATA_FILE.read_text())
    EARLIEST_FIRST_SEEN = min(
        (a["first_seen"] for a in data["agents"] if a.get("first_seen")), default=None
    )
    render_main()
    render_compare()


if __name__ == "__main__":
    main()
