cask "irrlicht" do
  version "0.6.10"
  sha256 "144989ca31cdc5b5dc6d6cf671b34da30124a5cc95952411b0e47c755d5bd0a6"

  url "https://github.com/ingo-eichhorst/Irrlicht/releases/download/v#{version}/Irrlicht-#{version}.dmg"
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
