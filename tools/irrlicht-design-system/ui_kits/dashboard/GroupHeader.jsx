// GroupHeader.jsx — collapsible project row
window.GroupHeader = function GroupHeader({ group, collapsed, onToggle, timeframe, onCycleTimeframe }) {
  const total = group.agents.length + group.agents.reduce((n,a) => n + (a.children?.length || 0), 0);
  const maxCtx = Math.max(0, ...group.agents.map(a => (a.metrics?.context_utilization_percentage) || 0));
  const dotColor = maxCtx > 90 ? 'var(--pressure-high)' : maxCtx > 75 ? 'var(--pressure-medium)' : maxCtx > 50 ? 'var(--waiting)' : 'var(--ready)';
  const tfSuffix = {day:'/d', week:'/w', month:'/mo', year:'/yr'}[timeframe] || '/d';
  const cost = group.costs && group.costs[timeframe];
  return (
    <div className="group-hdr">
      <button type="button" className="group-toggle" onClick={onToggle} aria-expanded={!collapsed}>
        <span className={'group-chevron' + (collapsed ? ' collapsed' : '')}>▾</span>
        <span className="group-dot" style={{background: dotColor}}/>
        <span className="group-name">{group.name}</span>
      </button>
      {cost > 0 && <button type="button" className="group-cost" onClick={onCycleTimeframe}>${cost.toFixed(2)}{tfSuffix}</button>}
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
