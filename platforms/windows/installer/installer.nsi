; Irrlicht Windows installer — NSIS script.
;
; Variables passed in via the build script:
;   /DVERSION    e.g. 0.3.8
;   /DARCH       amd64 | arm64
;   /DSTAGE_DIR  directory containing irrlicht.exe + irrlichd.exe
;   /DOUT_DIR    output directory for the produced setup.exe
;
; Behavior:
;   - Installs to %ProgramFiles%\Irrlicht
;   - Adds Start Menu shortcut, Add/Remove Programs entry
;   - Auto-start at login via HKCU Run key (mirrors macOS Login Items)
;   - Uninstaller stops both processes, removes files and registry entries

!define APP_NAME      "Irrlicht"
!define APP_PUBLISHER "Irrlicht Project"
!define APP_URL       "https://irrlicht.io"
!define BUNDLE_ID     "io.irrlicht.app"

; Compression — LZMA solid produces ~30% smaller installers than the default.
SetCompressor /SOLID lzma

Name "${APP_NAME} ${VERSION}"
OutFile "${OUT_DIR}\Irrlicht-${VERSION}-windows-${ARCH}-setup.exe"

; Default install dir uses the 64-bit Program Files even on arm64; Windows
; on ARM64 maps PROGRAMFILES64 to the native 64-bit location automatically.
InstallDir "$PROGRAMFILES64\${APP_NAME}"
InstallDirRegKey HKLM "Software\${APP_NAME}" "InstallDir"

RequestExecutionLevel admin
ShowInstDetails show
ShowUnInstDetails show

!include "MUI2.nsh"

!define MUI_ABORTWARNING
; Custom installer icon: deferred until we add a checked-in .ico under
; assets/ — NSIS falls back to its default until then.

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\irrlicht.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Launch Irrlicht now"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Section "Irrlicht (required)" SecCore
    SectionIn RO
    SetOutPath "$INSTDIR"

    ; Stop any running tray + daemon before overwriting binaries. Errors
    ; are silenced because a clean install has nothing to stop.
    nsExec::Exec 'taskkill /IM irrlicht.exe /F'
    nsExec::Exec 'taskkill /IM irrlichd.exe /F'

    File "${STAGE_DIR}\irrlicht.exe"
    File "${STAGE_DIR}\irrlichd.exe"
    File /nonfatal "${STAGE_DIR}\LICENSE.txt"

    ; Start Menu shortcut.
    CreateDirectory "$SMPROGRAMS\${APP_NAME}"
    CreateShortCut  "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk" "$INSTDIR\irrlicht.exe"

    ; Auto-start at login — HKCU so it follows the user across machines
    ; that share %INSTDIR% via roaming profiles.
    WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "Irrlicht" '"$INSTDIR\irrlicht.exe"'

    ; Add or Remove Programs entry.
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}" "DisplayName"     "${APP_NAME}"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}" "DisplayVersion"  "${VERSION}"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}" "Publisher"       "${APP_PUBLISHER}"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}" "URLInfoAbout"    "${APP_URL}"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}" "InstallLocation" "$INSTDIR"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}" "DisplayIcon"     "$INSTDIR\irrlicht.exe"
    WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}" "NoModify" 1
    WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}" "NoRepair" 1

    WriteRegStr HKLM "Software\${APP_NAME}" "InstallDir" "$INSTDIR"
    WriteRegStr HKLM "Software\${APP_NAME}" "Version"    "${VERSION}"

    WriteUninstaller "$INSTDIR\Uninstall.exe"
SectionEnd

Section "Uninstall"
    nsExec::Exec 'taskkill /IM irrlicht.exe /F'
    nsExec::Exec 'taskkill /IM irrlichd.exe /F'

    Delete "$INSTDIR\irrlicht.exe"
    Delete "$INSTDIR\irrlichd.exe"
    Delete "$INSTDIR\LICENSE.txt"
    Delete "$INSTDIR\Uninstall.exe"
    RMDir  "$INSTDIR"

    Delete "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk"
    RMDir  "$SMPROGRAMS\${APP_NAME}"

    DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "Irrlicht"
    DeleteRegKey   HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}"
    DeleteRegKey   HKLM "Software\${APP_NAME}"
SectionEnd
