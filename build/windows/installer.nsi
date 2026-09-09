; Per-user NSIS installer. Durable account/settings directories are never removed.
Unicode true
!include "MUI2.nsh"
!include "x64.nsh"
!include "WinVer.nsh"
!include "version.nsh"
!define APP_NAME "Switch Codex"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\Switch Codex"
Name "${APP_NAME}"
OutFile "${OUTPUT_FILE}"
InstallDir "$LOCALAPPDATA\Switch Codex"
InstallDirRegKey HKCU "${UNINSTALL_KEY}" "InstallLocation"
RequestExecutionLevel user
SetCompressor /SOLID lzma
ManifestDPIAware true
VIProductVersion "${APP_VERSION}.0"
VIAddVersionKey "ProductName" "${APP_NAME}"
VIAddVersionKey "ProductVersion" "${APP_VERSION}"
VIAddVersionKey "FileVersion" "${APP_VERSION}"
VIAddVersionKey "FileDescription" "Switch Codex Installer"
VIAddVersionKey "LegalCopyright" "Copyright © 2026 Switch Codex"
!define MUI_ICON "icon.ico"
!define MUI_UNICON "icon.ico"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "SimpChinese"
Function .onInit
  ${IfNot} ${AtLeastWin10}
    MessageBox MB_ICONSTOP "Windows 10 or later is required."
    Abort
  ${EndIf}
  ${IfNot} ${IsNativeAMD64}
    MessageBox MB_ICONSTOP "This installer requires Windows x64."
    Abort
  ${EndIf}
  SetRegView 64
  SetShellVarContext current
FunctionEnd
Section "Install"
  ReadRegStr $0 HKLM "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  ${If} $0 == ""
    ReadRegStr $0 HKCU "Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  ${EndIf}
  ${If} $0 == ""
    InitPluginsDir
    SetOutPath "$PLUGINSDIR"
    File "MicrosoftEdgeWebview2Setup.exe"
    ExecWait '"$PLUGINSDIR\MicrosoftEdgeWebview2Setup.exe" /silent /install' $0
    ${If} $0 != 0
      MessageBox MB_ICONSTOP "WebView2 installation failed. Connect to the internet and try again."
      SetErrorLevel 1
      Abort
    ${EndIf}
  ${EndIf}
  SetOutPath "$INSTDIR"
  ; NSIS offers abort/retry if an older process holds its executable open.
  File /oname=switch-codex.exe "${APP_BINARY}"
  WriteUninstaller "$INSTDIR\uninstall.exe"
  CreateShortcut "$SMPROGRAMS\Switch Codex.lnk" "$INSTDIR\switch-codex.exe"
  CreateShortcut "$DESKTOP\Switch Codex.lnk" "$INSTDIR\switch-codex.exe"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "${APP_NAME}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${APP_VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "Publisher" "Switch Codex"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\switch-codex.exe"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" '$\"$INSTDIR\uninstall.exe$\"'
  WriteRegStr HKCU "${UNINSTALL_KEY}" "QuietUninstallString" '$\"$INSTDIR\uninstall.exe$\" /S'
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoRepair" 1
SectionEnd
Section "Uninstall"
  SetRegView 64
  SetShellVarContext current
  Delete "$INSTDIR\switch-codex.exe"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\Switch Codex.lnk"
  Delete "$DESKTOP\Switch Codex.lnk"
  DeleteRegKey HKCU "${UNINSTALL_KEY}"
SectionEnd
