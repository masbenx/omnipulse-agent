# OmniPulse Agent - Windows

## Opsi A: Install dari release asset
```powershell
$Version="v1.0.0"
$Token=$env:GITHUB_TOKEN
Invoke-WebRequest -Headers @{ Authorization = "token $Token" } `
  -Uri "https://github.com/masbenx/omnipulse-agent/releases/download/$Version/omnipulse-agent-windows-amd64.exe" `
  -OutFile .\omnipulse-agent.exe
```

## Opsi B: Installer script (release)
```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Source release -Version latest -Token $env:GITHUB_TOKEN
```

## Opsi C: Build from source (release tag)
```powershell
git clone --branch v1.0.0 https://github.com/masbenx/omnipulse-agent.git
cd omnipulse-agent
$env:CGO_ENABLED="0"
go build -o omnipulse-agent.exe .
```

## Menjalankan (foreground)
```powershell
$env:OMNIPULSE_URL="https://monitor.company.com"
$env:AGENT_TOKEN="replace-with-agent-token"
$env:INTERVAL_SECONDS="10"
.\omnipulse-agent.exe run
```

## Service (Windows Service via kardianos/service)
Jalankan PowerShell sebagai Administrator.

Install service:
```powershell
.\omnipulse-agent.exe install --url "https://monitor.company.com" --token "AGENT_TOKEN" --interval 10
.\omnipulse-agent.exe start
```

Stop/uninstall:
```powershell
.\omnipulse-agent.exe stop
.\omnipulse-agent.exe uninstall
```

Catatan:
- Jika repo public, hapus header Authorization.
- Token sensitif; hindari menyimpan di history PowerShell.
