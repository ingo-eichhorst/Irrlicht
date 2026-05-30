cask "irrlicht" do
  version "0.4.8"
  sha256 "e3e3f01cd0135e9cce34b049a4966b49a9110cd0c44993022f254fbba9f8f867"

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

  # The cask install path doesn't register a LaunchAgent — the menu-bar app
  # spawns the embedded daemon itself. Power users who installed the
  # optional plist by hand can rm it themselves.
  uninstall quit: "io.irrlicht.app"

  zap trash: [
    "~/Library/Application Support/Irrlicht",
    "~/Library/Preferences/io.irrlicht.app.plist",
  ]
end
