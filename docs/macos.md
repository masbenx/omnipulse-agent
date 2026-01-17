# OmniPulse Agent - macOS

## Opsi 1: Installer script
```bash
curl -fsSL https://raw.githubusercontent.com/masbenx/omnipulse-agent/main/scripts/install.sh | sudo bash -s -- --source=git --version=main
```

## Opsi 2: Build from source
```bash
git clone https://github.com/masbenx/omnipulse-agent.git
cd omnipulse-agent
CGO_ENABLED=0 go build -o omnipulse-agent .
```

## Menjalankan (foreground)
```bash
OMNIPULSE_URL=https://monitor.company.com \
AGENT_TOKEN=replace-with-agent-token \
INTERVAL_SECONDS=10 \
./omnipulse-agent
```

Catatan:
- Integrasi service (launchd) akan disediakan pada milestone B7a.
