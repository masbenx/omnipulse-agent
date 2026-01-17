# OmniPulse Agent

Agent untuk mengirim metrics server (CPU/Mem/Disk/Net) ke backend OmniPulse via HTTPS.

## Config
Gunakan environment variables:
- `OMNIPULSE_URL` (contoh: https://monitor.company.com)
- `AGENT_TOKEN` (ditampilkan sekali saat create server)
- `INTERVAL_SECONDS` (default 10)

## Menjalankan (foreground)
```bash
OMNIPULSE_URL=https://monitor.company.com \
AGENT_TOKEN=replace-with-agent-token \
INTERVAL_SECONDS=10 \
./omnipulse-agent
```

## Instalasi
Lihat `docs/install.md` untuk opsi:
- Build from source via curl
- Build from source via git
- Installer script (`scripts/install.sh`, `scripts/install.ps1`)

## Service (Linux)
Lihat `docs/linux.md` dan `docs/hostinger-vm.md`.

## Catatan keamanan
- Jangan log/print `AGENT_TOKEN`.
- Gunakan HTTPS untuk produksi.
