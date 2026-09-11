cask "irrlicht" do
  version "0.6.3"
  sha256 "eb1518f24b4086a37e71989b3b2650da9653ef1f5e3c792e5b15b2641e496d34"

  url "https://github.com/ingo-eichhorst/Irrlicht/releases/download/v#{version}/Irrlicht-#{version}.dmg",
      verified: "github.com/ingo-eichhorst/Irrlicht/"
  name "Irrlicht"
  desc "Menu-bar telemetry for AI coding agents"
  homepage "https://irrlicht.io/"

  livecheck do
    url :url
    strategy :github_latest
  end

  depends_on macos: :ventura

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
