# Changelog

## [v1.2.1] - 2026-01-26
### Added
- Update contoh versi release pada docs ke v1.2.1 (tanpa perubahan runtime).

### Changed
- -

### Fixed
- -

### Security
- -

## [v1.2.0] - 2026-01-26
### Added
- Pengiriman metrik network per-interface ke `/api/ingest/server-network`.

### Changed
- Delta network dihitung per interface (skip loopback).

### Fixed
- -

### Security
- Token tetap tidak dicetak di log; gunakan env/args secukupnya.

## [v1.1.0] - 2026-01-19
### Added
- Perintah service: install/start/stop/uninstall via `kardianos/service`.
- Dokumentasi macOS/Windows untuk service.

### Changed
- CLI kini mendukung subcommand `run`.
- Contoh penggunaan foreground diperjelas dengan `run`.

### Fixed
- -

### Security
- Token tetap tidak dicetak di log; gunakan env/args secukupnya.
