// GasTown.jsx — the orchestrator panel
function stateColor(s) {
  if (s === 'working') return 'var(--working)';
  if (s === 'waiting') return 'var(--waiting)';
  if (s === 'ready') return 'var(--ready)';
  return 'var(--muted)';
}

function dotBar(total, done) {
  const max = 7;
  const t = Math.min(total, max);
  if (t <= 0) return '';
  const filled = total <= max ? done : Math.round(done/total*max);
  return '●'.repeat(Math.min(filled, t)) + '○'.repeat(Math.max(t - filled, 0));
}

window.GasTown = function GasTown({ orchestrator }) {
  if (!orchestrator || !orchestrator.running) return null;
  const convoys = (orchestrator.work_units || []).filter(w => w.type === 'convoy');
  return (
    <div className="gt-section">
      <div className="gt-header">
        <div className="gt-title"><span className="gt-emoji">⛽</span> Gas Town</div>
        <div className="gt-status-label"><div className="gt-status-dot" style={{background:'var(--ready)'}}/>running</div>
      </div>
      {orchestrator.global_agents?.length > 0 && (
        <div className="gt-agents-row">
          {orchestrator.global_agents.map((ga, i) => (
            <div key={i} className="gt-agent-chip" title={ga.description || ga.role}>
              <span>{ga.icon && ga.icon + ' '}{ga.role}</span>
              <span className="gt-chip-dot" style={{background: stateColor(ga.state)}}/>
              <span className="gt-chip-id">{ga.session_id ? ga.session_id.slice(0,6) : 'idle'}</span>
            </div>
          ))}
        </div>
      )}
      {orchestrator.codebases?.map((cb, ci) => {
        const workers = (cb.worktrees || []).flatMap(wt => wt.workers || []);
        if (!workers.length) return null;
        return (
          <div key={ci} className="gt-rig-block">
            <div className="gt-rig-name">rig: {cb.name}</div>
            <div className="gt-workers">
              {workers.map((w, wi) => (
                <div key={wi} className="gt-agent-chip" title={w.description || w.role}>
                  <span>{w.icon && w.icon + ' '}{w.role}{w.name && <span style={{color:'var(--text)'}}> {w.name}</span>}</span>
                  <span className="gt-chip-dot" style={{background: stateColor(w.state)}}/>
                  {w.id && <span className="gt-chip-id">{w.id.slice(0,8)}</span>}
                </div>
              ))}
            </div>
          </div>
        );
      })}
      {convoys.length > 0 && (
        <div className="gt-body">
          <div className="gt-convoy-header">🚚 Convoys</div>
          {convoys.map((c, i) => {
            const done = c.done >= c.total;
            return (
              <div key={i} className="gt-convoy-row">
                <span className={'gt-convoy-name' + (done ? ' done' : '')}>{c.name}</span>
                <span className="gt-dotbar" style={{color: done ? 'var(--ready)' : 'var(--working)'}}>{dotBar(c.total, c.done)}</span>
                <span className="gt-convoy-fraction">{c.done} / {c.total}</span>
                {done && <span className="gt-convoy-check">✓</span>}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
};
