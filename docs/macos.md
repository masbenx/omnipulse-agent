# OmniPulse Agent - macOS

## Opsi A: Install dari release asset
```bash
VERSION=v1.2.1
curl -L \
  -H "Authorization: token $GITHUB_TOKEN" \
  "https://github.com/masbenx/omnipulse-agent/releases/download/${VERSION}/omnipulse-agent-darwin-amd64" \
  -o omnipulse-agent
chmod +x omnipulse-agent
sudo install -m 0755 omnipulse-agent /usr/local/bin/omnipulse-agent
```

## Opsi B: Installer script (release)
```bash
curl -fsSL https://raw.githubusercontent.com/masbenx/omnipulse-agent/main/scripts/install.sh | \
  sudo bash -s -- --source=release --version=latest --token "$GITHUB_TOKEN"
```

## Opsi C: Build from source (release tag)
```bash
git clone --branch v1.2.1 https://github.com/masbenx/omnipulse-agent.git
cd omnipulse-agent
CGO_ENABLED=0 go build -o omnipulse-agent .
sudo install -m 0755 omnipulse-agent /usr/local/bin/omnipulse-agent
```

## Menjalankan (foreground)
```bash
OMNIPULSE_URL=https://monitor.company.com \
AGENT_TOKEN=replace-with-agent-token \
INTERVAL_SECONDS=10 \
./omnipulse-agent run
```

## Service (launchd via kardianos/service)
Install service:
```bash
sudo omnipulse-agent install --url "https://monitor.company.com" --token "AGENT_TOKEN" --interval 10
sudo omnipulse-agent start
```

Stop/uninstall:
```bash
sudo omnipulse-agent stop
sudo omnipulse-agent uninstall
```

Catatan:
- Jika repo public, hapus header Authorization.
- Untuk macOS ARM64, gunakan asset `omnipulse-agent-darwin-arm64`.
- Token sensitif; hindari menyimpan di shell history.
