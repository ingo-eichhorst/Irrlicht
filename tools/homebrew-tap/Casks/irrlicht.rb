cask "irrlicht" do
  version "0.3.10"
  sha256 "8cd1b1d9d916c2669271aa2d7bd00a70b5f4a8b6aa42a158ed06ffb98b6b5926"

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
