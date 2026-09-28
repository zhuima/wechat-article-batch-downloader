@echo off
setlocal

set "OUTPUT_DIR=%~dp0"
set "BINARY=%OUTPUT_DIR%mp_article_batch_downloader.exe"
set "TEMP_BINARY=%OUTPUT_DIR%mp_article_batch_downloader_windows_x86_64.exe"

if /I "%~1"=="windows" goto windows
if /I "%~1"=="windows-sunnynet" goto windows-sunnynet
if /I "%~1"=="all" goto all
goto usage

:windows
echo Building Windows x86_64...
set "CGO_ENABLED=0"
set "GOOS=windows"
set "GOARCH=amd64"
pushd "%~dp0.." || exit /b 1
go build -trimpath -ldflags="-s -w" -o "%TEMP_BINARY%" .
set "BUILD_RESULT=%ERRORLEVEL%"
popd
if not "%BUILD_RESULT%"=="0" exit /b %BUILD_RESULT%
move /Y "%TEMP_BINARY%" "%BINARY%" >nul
if errorlevel 1 exit /b 1
echo Done: %BINARY%
exit /b 0

:windows-sunnynet
echo Building Windows SunnyNet version...
echo This requires Docker on Windows:
echo   docker run --rm -v "%%cd%%:/workspace" -w /workspace golang:1.20 bash -c "...
echo Please run the Docker command manually from README.md
exit /b 1

:usage
echo Usage: build.bat [target]
echo   windows         - Windows x86_64
echo   windows-sunnynet - Windows SunnyNet ^(requires Docker^)
echo   all             - Build all targets
exit /b 1

:all
echo Building Windows...
call :windows
if errorlevel 1 exit /b 1
echo.
echo All done!
exit /b 0
