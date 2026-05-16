cask "irrlicht" do
  version "0.4.5"
  sha256 "6ace4040800cb0bc936433315a7446e5c1dded25dd004433b5b67a95e793b996"

  url "https://github.com/ingo-eichhorst/Irrlicht/releases/download/v#{version}/Irrlicht-#{version}.dmg",
      verified: "github.com/ingo-eichhorst/Irrlicht/"
  name "Irrlicht"
  desc "Menu-bar telemetry for AI coding agents"
  homepage "https://irrlicht.io/"

  livecheck do
    url :url
    strategy :github_latest
  end

  depends_on macos: ">= :ventura"

  app "Irrlicht.app"

  # The DMG is ad-hoc signed but not Apple-notarized. Strip the quarantine
  # attribute so Gatekeeper won't block first launch. Remove this once the
  # release flow notarizes (issue #187 follow-up).
  postflight do
    system_command "/usr/bin/xattr",
                   args: ["-dr", "com.apple.quarantine", "#{appdir}/Irrlicht.app"],
                   sudo: false
  end

  # The cask install path doesn't register a LaunchAgent — the menu-bar app
  # spawns the embedded daemon itself. Power users who installed the
  # optional plist by hand can rm it themselves.
  uninstall quit: "io.irrlicht.app"

  zap trash: [
    "~/Library/Application Support/Irrlicht",
    "~/Library/Preferences/io.irrlicht.app.plist",
  ]
end
