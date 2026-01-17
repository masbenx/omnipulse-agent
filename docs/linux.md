# OmniPulse Agent - Linux (systemd)

## Prasyarat
- Server sudah dibuat di OmniPulse dan punya `agent_token` (ditampilkan sekali).
- Backend OmniPulse bisa diakses via HTTPS (disarankan).

## Opsi A: Install dari release asset
```bash
VERSION=v1.0.0
curl -L \
  -H "Authorization: token $GITHUB_TOKEN" \
  "https://github.com/masbenx/omnipulse-agent/releases/download/${VERSION}/omnipulse-agent-linux-amd64" \
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
git clone --branch v1.0.0 https://github.com/masbenx/omnipulse-agent.git
cd omnipulse-agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o omnipulse-agent .
sudo install -m 0755 omnipulse-agent /usr/local/bin/omnipulse-agent
```

## Buat user service (disarankan)
```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin omnipulse
```

## Buat environment file
```bash
sudo tee /etc/omnipulse-agent.env >/dev/null <<'EOT'
OMNIPULSE_URL=https://monitor.company.com
AGENT_TOKEN=replace-with-agent-token
INTERVAL_SECONDS=10
EOT
sudo chmod 600 /etc/omnipulse-agent.env
sudo chown omnipulse:omnipulse /etc/omnipulse-agent.env
```

## Buat systemd unit
Simpan ke `/etc/systemd/system/omnipulse-agent.service`:
```ini
[Unit]
Description=OmniPulse Agent
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/omnipulse-agent.env
ExecStart=/usr/local/bin/omnipulse-agent
Restart=always
RestartSec=5
User=omnipulse
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

## Start dan enable
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now omnipulse-agent
sudo systemctl status omnipulse-agent --no-pager
```

## Cek log
```bash
sudo journalctl -u omnipulse-agent -f
```

Catatan keamanan:
- Jika repo public, hapus header Authorization.
- Jangan log/print `AGENT_TOKEN`.
- Gunakan HTTPS untuk produksi.
