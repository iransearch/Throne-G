@echo off
setlocal
cd /d "%~dp0"

set "CSC=%WINDIR%\Microsoft.NET\Framework64\v4.0.30319\csc.exe"
if not exist "%CSC%" set "CSC=%WINDIR%\Microsoft.NET\Framework\v4.0.30319\csc.exe"

if not exist "%CSC%" (
  echo .NET Framework C# compiler was not found.
  echo Install/enable .NET Framework 4.x, then run this file again.
  pause
  exit /b 1
)

echo Building ThroneBuilder 1.3.0-beta.1...
"%CSC%" /nologo /target:winexe /optimize+ /out:"%~dp0ThroneBuilder.exe" /reference:System.dll /reference:System.Drawing.dll /reference:System.Windows.Forms.dll "%~dp0tools\ThroneBuilder.cs"
set "BUILD_EXIT=%ERRORLEVEL%"

if not "%BUILD_EXIT%"=="0" (
  echo ThroneBuilder.exe build failed with exit code %BUILD_EXIT%.
  pause
  exit /b %BUILD_EXIT%
)

echo.
echo Created: %~dp0ThroneBuilder.exe
echo.
echo The GUI now offers:
echo   - ThroneCore - 3 target folders
echo   - SGuard - 4 files for IRSpeedyVPN
pause
exit /b 0
