# OmniPulse Agent

Agent untuk mengirim metrics server (CPU/Mem/Disk/Net) ke backend OmniPulse via HTTPS.

## Config
Gunakan environment variables:
- `OMNIPULSE_URL` (contoh: https://monitor.company.com)
- `AGENT_TOKEN` (ditampilkan sekali saat create server)
- `INTERVAL_SECONDS` (default 10)

## Instalasi (release asset)
```bash
VERSION=v1.0.0
curl -L \
  -H "Authorization: token $GITHUB_TOKEN" \
  "https://github.com/masbenx/omnipulse-agent/releases/download/${VERSION}/omnipulse-agent-linux-amd64" \
  -o omnipulse-agent
chmod +x omnipulse-agent
```
Verifikasi checksum: unduh `sha256sums.txt` dari release yang sama lalu cocokkan hash.

## Installer script (release)
```bash
curl -fsSL https://raw.githubusercontent.com/masbenx/omnipulse-agent/main/scripts/install.sh | \
  sudo bash -s -- --source=release --version=latest --token "$GITHUB_TOKEN"
```

## Menjalankan (foreground)
```bash
OMNIPULSE_URL=https://monitor.company.com \
AGENT_TOKEN=replace-with-agent-token \
INTERVAL_SECONDS=10 \
./omnipulse-agent
```

## Dokumentasi
- `docs/install.md`
- `docs/linux.md`
- `docs/hostinger-vm.md`
- `docs/macos.md`
- `docs/windows.md`

## Catatan keamanan
- Jika repo public, hapus header Authorization.
- Jangan log/print `AGENT_TOKEN`.
- Gunakan HTTPS untuk produksi.
