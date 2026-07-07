// GroupHeader.jsx — collapsible project row
window.GroupHeader = function GroupHeader({ group, collapsed, onToggle, timeframe, onCycleTimeframe }) {
  const total = group.agents.length + group.agents.reduce((n,a) => n + (a.children?.length || 0), 0);
  const maxCtx = Math.max(0, ...group.agents.map(a => (a.metrics?.context_utilization_percentage) || 0));
  const dotColor = maxCtx > 90 ? 'var(--pressure-high)' : maxCtx > 75 ? 'var(--pressure-medium)' : maxCtx > 50 ? 'var(--waiting)' : 'var(--ready)';
  const tfSuffix = {day:'/d', week:'/w', month:'/mo', year:'/yr'}[timeframe] || '/d';
  const cost = group.costs && group.costs[timeframe];
  const onToggleKeyDown = e => {
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onToggle(); }
  };
  const onCycleTimeframeKeyDown = e => {
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); e.stopPropagation(); onCycleTimeframe(); }
  };
  // SonarQube javascript:S6819 wants a real <button> instead of role="button"
  // here, but the row and the cost badge nest (the cost span sits inside the
  // row div) and a <button> can't validly contain another interactive
  // control — nesting two real <button>s would be invalid HTML. role="button"
  // + a keydown handler for Enter/Space is the documented WAI-ARIA pattern
  // for exactly this composite-widget case.
  return (
    <div className="group-hdr" onClick={onToggle} onKeyDown={onToggleKeyDown} role="button" tabIndex={0} aria-expanded={!collapsed}>
      <span className={'group-chevron' + (collapsed ? ' collapsed' : '')}>▾</span>
      <span className="group-dot" style={{background: dotColor}}/>
      <span className="group-name">{group.name}</span>
      {cost > 0 && <span className="group-cost" onClick={e => { e.stopPropagation(); onCycleTimeframe(); }} onKeyDown={onCycleTimeframeKeyDown} role="button" tabIndex={0}>${cost.toFixed(2)}{tfSuffix}</span>}
      <span className="group-count">{total} {total === 1 ? 'agent' : 'agents'}</span>
    </div>
  );
};

window.PressureAlert = function PressureAlert({ level }) {
  const cls = level === 'critical' ? 'alert-critical' : 'alert-high';
  return (
    <div className="pressure-alert-row">
      <span className={cls}>⚠ Switch to a fresh session soon</span>
    </div>
  );
};
