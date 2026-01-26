# OmniPulse Agent - Install di Hostinger VM (Linux)

Panduan ringkas untuk VM Hostinger berbasis Ubuntu/Debian.

## Opsi A: Download release asset
```bash
VERSION=v1.2.1
curl -L \
  -H "Authorization: token $GITHUB_TOKEN" \
  "https://github.com/masbenx/omnipulse-agent/releases/download/${VERSION}/omnipulse-agent-linux-amd64" \
  -o omnipulse-agent
chmod +x omnipulse-agent
```

## Opsi B: Build binary di lokal dan upload
```bash
cd /path/to/omnipulse-agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o omnipulse-agent .
```
Jika VM kamu ARM64, ganti `GOARCH=arm64`.

### Upload ke VM
```bash
scp ./omnipulse-agent root@<VM_IP>:/tmp/omnipulse-agent
```

## Pasang binary di VM
```bash
ssh root@<VM_IP>
sudo install -m 0755 /tmp/omnipulse-agent /usr/local/bin/omnipulse-agent
```

## Siapkan user + env + systemd
Ikuti langkah di `docs/linux.md` untuk:
- membuat user `omnipulse`
- membuat `/etc/omnipulse-agent.env`
- membuat unit `/etc/systemd/system/omnipulse-agent.service`

Pastikan `OMNIPULSE_URL` mengarah ke domain backend kamu (HTTPS).

## Start dan cek status
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now omnipulse-agent
sudo systemctl status omnipulse-agent --no-pager
```

## Verifikasi di backend
- Cek tabel `server_metrics` di Postgres
- Cek realtime WS `metrics:server:<id>`

Catatan jaringan:
- VM hanya perlu koneksi outbound ke backend (HTTPS).
