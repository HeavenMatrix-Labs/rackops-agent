@echo off
REM rackops-agent Windows service registration.
REM
REM Run from an elevated prompt in the directory containing rackops-agent.exe
REM and the prebuilt release zip's tls/ folder:
REM
REM   sc create rackops-agent binPath= %CD%\rackops-agent.exe -addr 127.0.0.1:8443 -tls-cert "%CD%\tls\agent.crt" -tls-key "%CD%\tls\agent.key"
REM   sc start rackops-agent
REM
REM Or simply run the agent directly for a foreground test:
REM
REM   rackops-agent.exe -addr 127.0.0.1:8443
REM
REM The installer also generates the token; show it with:
REM
REM   rackops-agent.exe -show-token

if not exist rackops-agent.exe (
    echo rackops-agent.exe not found in the current directory.
    echo Place this script next to the built agent binary and re-run.
    exit /b 1
)

echo Creating the rackops-agent Windows service...
sc.exe create rackops-agent binPath= "%CD%\rackops-agent.exe" start= auto
sc.exe description rackops-agent "RackOps per-host Agent (open source)"
sc.exe start rackops-agent

echo.
echo Service registered. Show the registration token with:
echo   rackops-agent.exe -show-token
echo Then paste it into the RackOps app to add this Host to your Lab.
