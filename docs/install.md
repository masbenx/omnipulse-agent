# Instalasi OmniPulse Agent

Dokumen ini menjelaskan opsi instalasi dari source dan installer.

## Opsi A: Build from source (curl)
```bash
export VERSION=main
curl -L "https://github.com/masbenx/omnipulse-agent/archive/refs/heads/${VERSION}.tar.gz" -o omnipulse-agent.tar.gz
mkdir -p omnipulse-agent-src
 tar -xzf omnipulse-agent.tar.gz -C omnipulse-agent-src --strip-components=1
cd omnipulse-agent-src

# Build binary
CGO_ENABLED=0 go build -o omnipulse-agent .
```

## Opsi B: Build from source (git)
```bash
git clone https://github.com/masbenx/omnipulse-agent.git
cd omnipulse-agent
CGO_ENABLED=0 go build -o omnipulse-agent .
```

## Opsi C: Installer script
### Linux/macOS (install.sh)
```bash
curl -fsSL https://raw.githubusercontent.com/masbenx/omnipulse-agent/main/scripts/install.sh | sudo bash -s -- --source=curl --version=main
```

### Windows (install.ps1)
```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Source git -Version main
```

Catatan:
- Installer ini melakukan build dari source, lalu memasang binary ke lokasi default.
- Untuk service Linux (systemd) lihat `docs/linux.md`.
- Untuk macOS/Windows, service manager disiapkan di milestone B7a.
