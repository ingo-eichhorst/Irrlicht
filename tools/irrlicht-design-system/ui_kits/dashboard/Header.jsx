// Header.jsx — sticky top bar
window.Header = function Header({ counts, wsStatus }) {
  return (
    <header>
      <div className="header-left">
        <div className="logo">irr<span>licht</span></div>
        <div className="summary" aria-label="Session summary">
          <div className="summary-item"><div className="summary-dot" style={{background:'var(--working)'}}/><span className="summary-count">{counts.working}</span></div>
          <div className="summary-item"><div className="summary-dot" style={{background:'var(--waiting)'}}/><span className="summary-count">{counts.waiting}</span></div>
          <div className="summary-item"><div className="summary-dot" style={{background:'var(--ready)'}}/><span className="summary-count">{counts.ready}</span></div>
        </div>
      </div>
      <div className="ws-status" role="status" aria-live="polite">
        <div className={'ws-ring ' + wsStatus}/>
        <span>{wsStatus}</span>
      </div>
    </header>
  );
};
