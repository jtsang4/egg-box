; The installer of Sing-Box UI for Windows.
;
; It installs for the whole computer, not for one person, because the network
; service it sets up runs as the system: both programs go under Program Files,
; where only administrators can change them (the service refuses to run from
; anywhere else), and the service is registered with the system, with a data
; folder only administrators can write in.
;
; Built by packaging/windows/build.sh:
;   makensis -DVERSION=1.2.3 -DARCH=amd64 -DAGENT=agentd.exe -DGUI=app.exe -DOUTFILE=setup.exe installer.nsi
;
; Run it with /S for an installation without questions.

Unicode true
ManifestDPIAware true
SetCompressor /SOLID lzma

!ifndef VERSION
  !error "pass -DVERSION=..."
!endif
!ifndef VERSION_NUMERIC
  !define VERSION_NUMERIC "${VERSION}.0"   ; four numbers; the build script strips a suffix such as -rc1
!endif
!ifndef ARCH
  !error "pass -DARCH=amd64 or arm64"
!endif
!ifndef AGENT
  !error "pass -DAGENT=path of agentd.exe"
!endif
!ifndef GUI
  !error "pass -DGUI=path of the application's exe"
!endif
!ifndef OUTFILE
  !define OUTFILE "SingBoxUI-Setup-${VERSION}-${ARCH}.exe"
!endif

!define NAME      "Sing-Box UI"
!define SLUG      "SingBoxUI"
!define GUI_EXE   "Sing-Box UI.exe"       ; the service looks for this exact name next to itself
!define AGENT_EXE "agentd.exe"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${SLUG}"

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "x64.nsh"
!include "FileFunc.nsh"

Name "${NAME}"
OutFile "${OUTFILE}"
RequestExecutionLevel admin
InstallDir "$PROGRAMFILES64\${SLUG}"
InstallDirRegKey HKLM "Software\${SLUG}" "InstallDir"
BrandingText "${NAME} ${VERSION}"
VIProductVersion "${VERSION_NUMERIC}"
VIAddVersionKey "ProductName" "${NAME}"
VIAddVersionKey "FileDescription" "${NAME} installer"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "(c) 2026 jtsang"

!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${GUI_EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "Open ${NAME}"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "SimpChinese"

Function .onInit
  ${If} "${ARCH}" == "amd64"
  ${AndIfNot} ${RunningX64}
    MessageBox MB_OK|MB_ICONSTOP "${NAME} needs a 64-bit version of Windows."
    Abort
  ${EndIf}
  SetRegView 64
FunctionEnd

Function un.onInit
  SetRegView 64
FunctionEnd

Section "Install"
  SetRegView 64

  ; An earlier version may be running: stopping the service disconnects and
  ; puts the network back before its program is replaced.
  ${If} ${FileExists} "$INSTDIR\${AGENT_EXE}"
    nsExec::ExecToLog '"$INSTDIR\${AGENT_EXE}" stop'
    Pop $0
  ${EndIf}

  SetOutPath "$INSTDIR"
  File "/oname=${AGENT_EXE}" "${AGENT}"
  File "/oname=${GUI_EXE}" "${GUI}"
  WriteUninstaller "$INSTDIR\Uninstall.exe"

  ; Register the service. Failing here is the one thing that must stop the
  ; installation: without the service the application can do nothing.
  nsExec::ExecToLog '"$INSTDIR\${AGENT_EXE}" install'
  Pop $0
  ${If} $0 != 0
    MessageBox MB_OK|MB_ICONSTOP "The network service could not be set up (code $0). Nothing else was changed."
    Abort
  ${EndIf}

  ; Let the person who is signed in use it. If nobody is (an unattended
  ; installation), that is left to the application, which asks the first time.
  nsExec::ExecToLog '"$INSTDIR\${AGENT_EXE}" authorize'
  Pop $0

  CreateDirectory "$SMPROGRAMS\${NAME}"
  CreateShortcut "$SMPROGRAMS\${NAME}\${NAME}.lnk" "$INSTDIR\${GUI_EXE}"

  WriteRegStr HKLM "Software\${SLUG}" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayName" "${NAME}"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNINSTKEY}" "Publisher" "jtsang"
  WriteRegStr HKLM "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\${GUI_EXE}"
  WriteRegStr HKLM "${UNINSTKEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegStr HKLM "${UNINSTKEY}" "QuietUninstallString" '"$INSTDIR\Uninstall.exe" /S'
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKLM "${UNINSTKEY}" "EstimatedSize" "$0"
SectionEnd

Section "Uninstall"
  SetRegView 64

  ; Stopping disconnects and puts the network back, and removing the service
  ; takes its data folder with it. A failure is not a reason to leave the
  ; program installed; the service's own cleanup looks for leftovers.
  ${If} ${FileExists} "$INSTDIR\${AGENT_EXE}"
    nsExec::ExecToLog '"$INSTDIR\${AGENT_EXE}" uninstall --purge'
    Pop $0
  ${EndIf}

  Delete "$INSTDIR\${GUI_EXE}"
  Delete "$INSTDIR\${AGENT_EXE}"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"

  Delete "$SMPROGRAMS\${NAME}\${NAME}.lnk"
  RMDir "$SMPROGRAMS\${NAME}"

  DeleteRegKey HKLM "${UNINSTKEY}"
  DeleteRegKey HKLM "Software\${SLUG}"
SectionEnd
