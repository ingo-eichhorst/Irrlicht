// Hero.jsx — oversized serif wordmark, phonetic, tagline
window.Hero = function Hero() {
  return (
    <section className="hero">
      <div className="hero-eyebrow">menu-bar telemetry for claude code</div>
      <h1 className="hero-word">Irrlicht</h1>
      <div className="hero-pron">/ ˈɪʁˌlɪçt / <span>· will-o'-the-wisp</span></div>
      <p className="hero-tag">
        Three lights in your macOS menu bar. <br/>
        <em>Working</em>, <em>waiting</em>, <em>ready</em> — at a glance, across every session.
      </p>
    </section>
  );
};

window.LightsSignature = function LightsSignature() {
  return (
    <div className="lights">
      <div className="light">
        <div className="orb" style={{background:'#8B5CF6',boxShadow:'0 0 24px #8B5CF6,0 0 80px rgba(139,92,246,0.45)'}}/>
        <div className="label">working</div>
        <div className="caption">the agent is typing, reading, editing, thinking</div>
      </div>
      <div className="light">
        <div className="orb" style={{background:'#FF9500',boxShadow:'0 0 24px #FF9500,0 0 80px rgba(255,149,0,0.4)'}}/>
        <div className="label">waiting</div>
        <div className="caption">it needs you — a question, a review, a decision</div>
      </div>
      <div className="light">
        <div className="orb" style={{background:'#34C759',boxShadow:'0 0 24px #34C759,0 0 80px rgba(52,199,89,0.4)'}}/>
        <div className="label">ready</div>
        <div className="caption">idle. the next thing is yours to start</div>
      </div>
    </div>
  );
};
