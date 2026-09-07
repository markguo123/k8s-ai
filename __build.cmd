@REM Windows CMD 构建脚本：版本注入 + 编译二进制
@echo off
setlocal

set "VERSION=v2.0.8"
set "BIN_DIR=bin"
set "BINARY=k8s-ai"
set "MODULE=github.com/k8s-ai/k8s-ai"

set "COMMIT=none"
for /f "delims=" %%H in ('git rev-parse --short HEAD 2^>nul') do set "COMMIT=%%H"

for /f "tokens=2 delims==" %%I in ('wmic os get localdatetime /value ^| find "="') do set "DT=%%I"
set "DATE=%DT:~0,4%-%DT:~4,2%-%DT:~6,2%T%DT:~8,2%:%DT:~10,2%:%DT:~12,2%Z"

if not exist "%BIN_DIR%" mkdir "%BIN_DIR%"

echo ==^> build  k8s-ai  version=%VERSION%  commit=%COMMIT%  date=%DATE%

go build -ldflags "-s -w -X %MODULE%/internal/version.Version=%VERSION% -X %MODULE%/internal/version.Commit=%COMMIT% -X %MODULE%/internal/version.Date=%DATE%" -o "%BIN_DIR%\%BINARY%.exe" ./cmd/k8s-ai
if errorlevel 1 exit /b %errorlevel%

echo ==^> binary: %~dp0%BIN_DIR%\%BINARY%.exe
"%BIN_DIR%\%BINARY%.exe" version
