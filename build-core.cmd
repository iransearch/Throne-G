@echo off
setlocal
cd /d "%~dp0"

if /I "%~1"=="--core" goto BUILD_CORE
if /I "%~1"=="--sguard" goto BUILD_SGUARD
if /I "%~1"=="--gui" goto BUILD_GUI

echo ThroneBuilder
echo ==========================
echo.
echo [1] ThroneCore - 3 target folders
echo [2] SGuard - 4 files for IRSpeedyVPN
echo [3] Full Throne - GUI + Core, Windows x64
echo.
set /p "BUILD_CHOICE=Select output [1/2/3]: "
if "%BUILD_CHOICE%"=="3" goto BUILD_GUI
if "%BUILD_CHOICE%"=="2" goto BUILD_SGUARD
if "%BUILD_CHOICE%"=="1" goto BUILD_CORE

echo Invalid selection.
pause >nul
exit /b 2

:BUILD_CORE
echo.
echo ThroneCore three-target builder
echo ===============================
echo.

if /I "%THRONE_CORE_VALIDATE_ONLY%"=="1" (
  powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0tools\Build-ThroneCore.ps1" -ValidateOnly
  exit /b %ERRORLEVEL%
)

powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0tools\Build-ThroneCore.ps1"
set "BUILD_EXIT=%ERRORLEVEL%"
set "BUILD_OUTPUT=%~dp0CoreBuilds"
goto BUILD_DONE

:BUILD_SGUARD
echo.
echo IRSpeedy SGuard four-file builder
echo ================================
echo.

if /I "%THRONE_CORE_VALIDATE_ONLY%"=="1" (
  powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0tools\Build-SGuard.ps1" -ValidateOnly
  exit /b %ERRORLEVEL%
)

powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0tools\Build-SGuard.ps1"
set "BUILD_EXIT=%ERRORLEVEL%"
set "BUILD_OUTPUT=%~dp0SGuardBuilds"
goto BUILD_DONE

:BUILD_GUI
echo.
echo Full Throne portable Windows x64 builder
echo =========================================
if /I "%THRONE_CORE_VALIDATE_ONLY%"=="1" (
  powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0tools\Build-ThroneWindows.ps1" -ValidateOnly
  goto VALIDATE_DONE
)
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0tools\Build-ThroneWindows.ps1"
set "BUILD_EXIT=%ERRORLEVEL%"
set "BUILD_OUTPUT=%~dp0ThroneBuilds"
goto BUILD_DONE

:VALIDATE_DONE
exit /b %ERRORLEVEL%

:BUILD_DONE
echo.
if not "%BUILD_EXIT%"=="0" (
  echo BUILD FAILED with exit code %BUILD_EXIT%.
  echo Read the error above, then press any key to close.
) else (
  echo BUILD SUCCEEDED.
  echo Outputs are in: %BUILD_OUTPUT%
  echo Press any key to close.
)
pause >nul
exit /b %BUILD_EXIT%
