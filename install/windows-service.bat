@echo off
REM rackops-agent Windows service registration.
REM
REM Run from an elevated prompt in the directory containing rackops-agent.exe:
REM
REM   install\windows-service.bat
REM
REM This registers the Agent as a Windows service (sc.exe), generates a
REM self-signed TLS pair next to the binary if one is not already there,
REM and points the service at the same token file the installer reports.
REM
REM Re-running this file is the in-place update path: it stops the
REM service, replaces the binary, and re-registers.

setlocal enabledelayedexpansion

if not exist rackops-agent.exe (
    echo rackops-agent.exe not found in the current directory.
    echo Place this script next to the built agent binary and re-run.
    exit /b 1
)

REM The service stops first: installing over a running image fails.
sc.exe stop rackops-agent >nul 2>&1

REM TLS material. Generated once and then preserved across updates.
if not exist "tls\tls.crt" (
    echo Generating self-signed TLS certificate...
    if not exist tls mkdir tls
    REM openssl ships with Git for Windows and with most dev toolchains;
    REM without it the Agent still runs, just without TLS.
    where openssl >nul 2>&1
    if %errorlevel% equ 0 (
        openssl req -x509 -newkey rsa:2048 -nodes -keyout "tls\tls.key" -out "tls\tls.crt" -days 825 -subj "/CN=rackops-agent" >nul 2>&1
    ) else (
        echo WARNING: openssl not found. Skipping TLS generation.
        echo The Agent will serve plain HTTP until you supply -tls-cert/-tls-key.
    )
)

REM Mint the registration token at the path the service will read, so the
REM value printed below is the one the Agent actually enforces.
rackops-agent.exe -show-token -token "tls\token"
if errorlevel 1 (
    echo ERROR: could not create the registration token.
    exit /b 1
)

set ARGS=-addr 127.0.0.1:8443 -mode full
if exist "tls\tls.crt" set ARGS=!ARGS! -tls-cert "%CD%\tls\tls.crt" -tls-key "%CD%\tls\tls.key"
set ARGS=!ARGS! -token "%CD%\tls\token"

echo Registering the rackops-agent Windows service...
sc.exe delete rackops-agent >nul 2>&1
sc.exe create rackops-agent binPath= "%CD%\rackops-agent.exe" !ARGS! start= auto DisplayName= "RackOps Agent"
sc.exe description rackops-agent "RackOps per-host Agent (open source)"
sc.exe start rackops-agent

echo.
echo Service registered. Run "rackops-agent.exe -show-token -token tls\token"
echo to read the registration token again, then paste it into the RackOps
echo app to add this Host to your Lab.