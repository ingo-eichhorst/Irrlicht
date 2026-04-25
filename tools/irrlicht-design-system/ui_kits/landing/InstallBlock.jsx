// InstallBlock.jsx — terminal pill with brew command
window.InstallBlock = function InstallBlock() {
  const [copied, setCopied] = React.useState(false);
  const cmd = 'brew install --cask ingo-eichhorst/tap/irrlicht';
  const copy = () => {
    navigator.clipboard?.writeText(cmd);
    setCopied(true);
    setTimeout(() => setCopied(false), 1400);
  };
  return (
    <div className="install" id="install">
      <div className="install-label">one command. no account.</div>
      <div className="install-pill">
        <span className="install-prompt">$</span>
        <code className="install-cmd">{cmd}</code>
        <button className="install-copy" onClick={copy}>{copied ? '✓ copied' : 'copy'}</button>
      </div>
      <div className="install-foot">macOS 13+ · ~5 MB · Apple Silicon &amp; Intel</div>
    </div>
  );
};

window.FeatureTrio = function FeatureTrio() {
  const features = [
    { dot: '#8B5CF6', title: 'Lives in your menu bar', body: 'A pill per project, a dot per session. No dock icon. No window to minimise.' },
    { dot: '#FF9500', title: 'Real-time, not polled', body: 'Tails Claude Code\u2019s session logs directly. Updates in under a second, with sub-MB overhead.' },
    { dot: '#34C759', title: 'Zero configuration', body: 'Install it. It finds your sessions. That is the onboarding.' },
  ];
  return (
    <section className="features" id="features">
      {features.map((f, i) => (
        <div key={i} className="feature">
          <div className="feature-dot" style={{background: f.dot, boxShadow: `0 0 14px ${f.dot}`}}/>
          <h3 className="feature-title">{f.title}</h3>
          <p className="feature-body">{f.body}</p>
        </div>
      ))}
    </section>
  );
};
