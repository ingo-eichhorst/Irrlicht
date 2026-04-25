// SiteHeader.jsx
window.SiteHeader = function SiteHeader() {
  return (
    <header className="site-header">
      <div className="wordmark-sm">irr<span>licht</span></div>
      <nav>
        <a href="#features">features</a>
        <a href="#install">install</a>
        <a href="https://github.com/ingo-eichhorst/Irrlicht" className="gh">github ↗</a>
      </nav>
    </header>
  );
};

window.SiteFooter = function SiteFooter() {
  return (
    <footer className="site-footer">
      <div className="etym">
        <span className="word">Irrlicht</span>
        <span className="pron">/ ˈɪʁˌlɪçt /</span>
        <span className="pos">n.</span>
        <span className="def">a pale flame seen hovering over marshy ground at night — a will-o'-the-wisp.</span>
      </div>
      <div className="colophon">Built in the dark by ingo-eichhorst · MIT</div>
    </footer>
  );
};
