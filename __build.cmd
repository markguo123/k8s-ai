@REM Windows CMD 构建脚本：版本注入 + 编译二进制（Windows + Linux）
@echo off
setlocal

set "VERSION=v3.0.0-beta"
set "BIN_DIR=bin"
set "BINARY=k8s-ai"
set "MODULE=github.com/k8s-ai/k8s-ai"

set "COMMIT=none"
for /f "delims=" %%H in ('git rev-parse --short HEAD 2^>nul') do set "COMMIT=%%H"

for /f "usebackq delims=" %%I in (`powershell -NoProfile -Command "[DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')"`) do set "DATE=%%I"

if not exist "%BIN_DIR%" mkdir "%BIN_DIR%"

echo ==^> build windows amd64        version=%VERSION%  commit=%COMMIT%  date=%DATE%
go build -ldflags "-s -w -X %MODULE%/internal/version.Version=%VERSION% -X %MODULE%/internal/version.Commit=%COMMIT% -X %MODULE%/internal/version.Date=%DATE%" -o "%BIN_DIR%\%BINARY%.exe" ./cmd/k8s-ai
if errorlevel 1 exit /b %errorlevel%

echo ==^> build linux amd64          version=%VERSION%  commit=%COMMIT%  date=%DATE%
setlocal
set "CGO_ENABLED=0"
set "GOOS=linux"
set "GOARCH=amd64"
go build -ldflags "-s -w -X %MODULE%/internal/version.Version=%VERSION% -X %MODULE%/internal/version.Commit=%COMMIT% -X %MODULE%/internal/version.Date=%DATE%" -o "%BIN_DIR%\%BINARY%-linux-amd64" ./cmd/k8s-ai
endlocal
if errorlevel 1 exit /b %errorlevel%

echo ==^> binaries:
for %%f in ("%BIN_DIR%\%BINARY%.exe" "%BIN_DIR%\%BINARY%-linux-amd64") do echo           %%~ff
"%BIN_DIR%\%BINARY%.exe" version
