// SessionRow.jsx — one agent row
const stateIcons = {
  working: <svg className="working" viewBox="0 0 16 16" fill="none" width="12" height="12"><circle cx="8" cy="8" r="6.5" stroke="#8B5CF6" strokeWidth="1.5" strokeDasharray="4 3"/></svg>,
  waiting: <svg viewBox="0 0 16 16" fill="none" width="12" height="12"><rect x="4" y="3" width="2.5" height="10" rx="1" fill="#FF9500"/><rect x="9.5" y="3" width="2.5" height="10" rx="1" fill="#FF9500"/></svg>,
  ready: <svg viewBox="0 0 16 16" fill="none" width="12" height="12"><circle cx="8" cy="8" r="6.5" stroke="#34C759" strokeWidth="1.5"/><path d="M5 8.2l2 2 4-4.4" stroke="#34C759" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"/></svg>,
  cancelled: <svg viewBox="0 0 16 16" fill="none" width="12" height="12"><circle cx="8" cy="8" r="6.5" stroke="#8E8E93" strokeWidth="1.5"/><path d="M5.5 5.5l5 5M10.5 5.5l-5 5" stroke="#8E8E93" strokeWidth="1.5" strokeLinecap="round"/></svg>,
};

function pressureClass(p) {
  if (p === 'critical') return 'critical';
  if (p === 'high' || p === 'warning') return 'high';
  if (p === 'medium' || p === 'caution') return 'medium';
  return '';
}

window.SessionRow = function SessionRow({ agent, num, isChild }) {
  const m = agent.metrics || {};
  const state = agent.state || 'ready';
  const isActive = state === 'working' || state === 'waiting';
  const ctx = m.context_utilization_percentage || 0;
  const pCls = pressureClass(m.pressure_level);
  const subCount = (agent.children || []).filter(c => c.state === 'working' || c.state === 'waiting').length;
  const toolLbl = { Edit:'Edit', Read:'Read', Bash:'Bash', AskUserQuestion:'Ask User', ExitPlanMode:'Plan Mode', Grep:'Grep', Write:'Write' };
  const tool = m.has_open_tool_call && m.last_open_tool_names && m.last_open_tool_names[0];
  const toolIsUser = tool === 'AskUserQuestion' || tool === 'ExitPlanMode';

  return (
    <div className={'session-row' + (isChild ? ' child' : '')}>
      <span className="row-state-icon">{stateIcons[state]}</span>
      {agent.role && <span className="row-role-badge" title={agent.description || agent.role}>{agent.icon ? agent.icon + ' ' : ''}{agent.role}</span>}
      <span className="row-num">{num}</span>
      {subCount > 0 && <span className="row-sub-badge">{subCount}</span>}
      <span className="row-branch">{agent.git_branch || '—'}</span>
      {state === 'waiting' && m.last_assistant_text && <span className="row-question" title={m.last_assistant_text}>{m.last_assistant_text.slice(0, 100)}{m.last_assistant_text.length > 100 ? '…' : ''}</span>}
      <span className="row-ctx-bar"><span className={'row-ctx-fill ' + pCls} style={{width: Math.min(100, ctx) + '%'}}/></span>
      <span className="row-ctx-pct" style={{color: ctx > 0 ? `var(--pressure-${pCls || 'low'})` : ''}}>{ctx > 0 ? ctx.toFixed(0) + '%' : ''}</span>
      <span className="row-cost">{m.estimated_cost_usd ? '$' + m.estimated_cost_usd.toFixed(2) : ''}</span>
      {tool && state === 'working' && <span className={'row-tool' + (toolIsUser ? ' tool-user' : '')}>{toolLbl[tool] || tool}</span>}
      <span className="row-spacer"/>
      <span className="row-model">{(m.model_name || '').replace(/^claude-/,'').replace(/-(\d)/,'.$1')}</span>
      <span className="row-elapsed">{isActive && m.elapsed_seconds ? fmtDur(m.elapsed_seconds) : ''}</span>
      <span className="row-id">{(agent.session_id || '').slice(0,6)}</span>
    </div>
  );
};

function fmtDur(s) {
  const h = Math.floor(s/3600), m = Math.floor((s%3600)/60), sec = s%60;
  if (h) return h+'h'+m+'m';
  if (m) return m+'m'+sec+'s';
  return sec+'s';
}
window.fmtDur = fmtDur;
