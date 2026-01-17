# OmniPulse Agent - Windows

## Opsi 1: Installer script (PowerShell)
```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Source git -Version main
```

## Opsi 2: Build from source
```powershell
git clone https://github.com/masbenx/omnipulse-agent.git
cd omnipulse-agent
$env:CGO_ENABLED="0"
go build -o omnipulse-agent.exe .
```

## Menjalankan (foreground)
```powershell
$env:OMNIPULSE_URL="https://monitor.company.com"
$env:AGENT_TOKEN="replace-with-agent-token"
$env:INTERVAL_SECONDS="10"
.\omnipulse-agent.exe
```

Catatan:
- Integrasi Windows Service akan disediakan pada milestone B7a.
