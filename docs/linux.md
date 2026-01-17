# OmniPulse Agent - Linux (systemd)

## Prasyarat
- Server sudah dibuat di OmniPulse dan punya `agent_token` (ditampilkan sekali).
- Backend OmniPulse bisa diakses via HTTPS (disarankan).
- Go 1.22+ tersedia untuk build binary.

## 1) Build from source (curl)
```bash
export VERSION=main
curl -L "https://github.com/masbenx/omnipulse-agent/archive/refs/heads/${VERSION}.tar.gz" -o omnipulse-agent.tar.gz
mkdir -p omnipulse-agent-src
 tar -xzf omnipulse-agent.tar.gz -C omnipulse-agent-src --strip-components=1
cd omnipulse-agent-src
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o omnipulse-agent .
```

## 2) Build from source (git)
```bash
git clone https://github.com/masbenx/omnipulse-agent.git
cd omnipulse-agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o omnipulse-agent .
```

## 3) Pasang binary
```bash
sudo install -m 0755 omnipulse-agent /usr/local/bin/omnipulse-agent
```

## 4) Buat user service (disarankan)
```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin omnipulse
```

## 5) Buat environment file
```bash
sudo tee /etc/omnipulse-agent.env >/dev/null <<'EOT'
OMNIPULSE_URL=https://monitor.company.com
AGENT_TOKEN=replace-with-agent-token
INTERVAL_SECONDS=10
EOT
sudo chmod 600 /etc/omnipulse-agent.env
sudo chown omnipulse:omnipulse /etc/omnipulse-agent.env
```

## 6) Buat systemd unit
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

## 7) Start dan enable
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now omnipulse-agent
sudo systemctl status omnipulse-agent --no-pager
```

## 8) Cek log
```bash
sudo journalctl -u omnipulse-agent -f
```

Catatan keamanan:
- Jangan log/print `AGENT_TOKEN`.
- Gunakan HTTPS untuk produksi.
