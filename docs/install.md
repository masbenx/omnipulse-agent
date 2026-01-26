# Instalasi OmniPulse Agent

Dokumen ini menjelaskan opsi instalasi dari release asset dan build from source.

## Opsi A: Install dari Release Asset (disarankan)
Untuk repo private, tambahkan header Authorization token.

### Linux/macOS
```bash
VERSION=v1.1.0
curl -L \
  -H "Authorization: token $GITHUB_TOKEN" \
  "https://github.com/masbenx/omnipulse-agent/releases/download/${VERSION}/omnipulse-agent-linux-amd64" \
  -o omnipulse-agent
chmod +x omnipulse-agent
```

### Windows
```powershell
$Version="v1.1.0"
$Token=$env:GITHUB_TOKEN
Invoke-WebRequest -Headers @{ Authorization = "token $Token" } `
  -Uri "https://github.com/masbenx/omnipulse-agent/releases/download/$Version/omnipulse-agent-windows-amd64.exe" `
  -OutFile .\omnipulse-agent.exe
```

Catatan:
- Jika repo public, hapus header Authorization.
- Ganti `linux-amd64` sesuai arsitektur (`linux-arm64`, `darwin-amd64`, `darwin-arm64`).
- Verifikasi checksum: unduh `sha256sums.txt` dari release yang sama dan cocokkan hash.

Contoh verifikasi checksum:
```bash
VERSION=v1.1.0
curl -L -H "Authorization: token $GITHUB_TOKEN" \
  "https://github.com/masbenx/omnipulse-agent/releases/download/${VERSION}/sha256sums.txt" \
  -o sha256sums.txt
sha256sum -c sha256sums.txt --ignore-missing
```

## Opsi B: Installer script (release)
### Linux/macOS (install.sh)
```bash
curl -fsSL https://raw.githubusercontent.com/masbenx/omnipulse-agent/main/scripts/install.sh | \
  sudo bash -s -- --source=release --version=latest --token "$GITHUB_TOKEN"
```

### Windows (install.ps1)
```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Source release -Version latest -Token $env:GITHUB_TOKEN
```

## Opsi C: Build from source (release tag)
### curl
```bash
VERSION=v1.1.0
curl -L "https://github.com/masbenx/omnipulse-agent/archive/refs/tags/${VERSION}.tar.gz" -o omnipulse-agent.tar.gz
mkdir -p omnipulse-agent-src
 tar -xzf omnipulse-agent.tar.gz -C omnipulse-agent-src --strip-components=1
cd omnipulse-agent-src
CGO_ENABLED=0 go build -o omnipulse-agent .
```

### git
```bash
git clone --branch v1.1.0 https://github.com/masbenx/omnipulse-agent.git
cd omnipulse-agent
CGO_ENABLED=0 go build -o omnipulse-agent .
```

Catatan:
- Installer release tidak membutuhkan Go di host target.
- Build from source membutuhkan Go 1.22+.
- Untuk service Linux (systemd) lihat `docs/linux.md`.
- Untuk macOS/Windows, gunakan perintah service bawaan agent.

## Service (macOS/Windows)
Install service dengan membawa konfigurasi (URL/token/interval):
```bash
sudo omnipulse-agent install --url "https://monitor.company.com" --token "AGENT_TOKEN" --interval 10
sudo omnipulse-agent start
```

Stop/uninstall:
```bash
sudo omnipulse-agent stop
sudo omnipulse-agent uninstall
```
