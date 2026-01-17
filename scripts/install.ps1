param(
  [ValidateSet("release", "git", "zip")] [string]$Source = "release",
  [string]$Version = "latest",
  [string]$Prefix = "C:\Program Files\OmniPulse",
  [string]$Token = ""
)

$ErrorActionPreference = "Stop"

function Require-Command($name) {
  if (-not (Get-Command $name -ErrorAction SilentlyContinue)) {
    throw "$name is required"
  }
}

function Get-LatestTag($token) {
  $headers = @{}
  if ($token) { $headers["Authorization"] = "token $token" }
  $resp = Invoke-RestMethod -Uri "https://api.github.com/repos/masbenx/omnipulse-agent/releases/latest" -Headers $headers
  if (-not $resp.tag_name) { throw "unable to resolve latest release tag" }
  return $resp.tag_name
}

function Get-Arch() {
  if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { return "arm64" }
  return "amd64"
}

if ($Source -eq "release") {
  $tag = $Version
  if ($Version -eq "latest") { $tag = Get-LatestTag $Token }
  $arch = Get-Arch
  $asset = "omnipulse-agent-windows-$arch.exe"
  $url = "https://github.com/masbenx/omnipulse-agent/releases/download/$tag/$asset"
  $headers = @{}
  if ($Token) { $headers["Authorization"] = "token $Token" }

  $tmp = New-Item -ItemType Directory -Path ([System.IO.Path]::Combine([System.IO.Path]::GetTempPath(), "omnipulse-agent" + [System.Guid]::NewGuid().ToString("N")))
  $dest = Join-Path $tmp $asset
  Invoke-WebRequest -Uri $url -OutFile $dest -Headers $headers

  New-Item -ItemType Directory -Path $Prefix -Force | Out-Null
  Copy-Item -Path $dest -Destination (Join-Path $Prefix "omnipulse-agent.exe") -Force
  Write-Output "installed to $Prefix\omnipulse-agent.exe"
  exit 0
}

Require-Command go

$workDir = New-Item -ItemType Directory -Path ([System.IO.Path]::Combine([System.IO.Path]::GetTempPath(), "omnipulse-agent" + [System.Guid]::NewGuid().ToString("N")))
$repoDir = Join-Path $workDir "omnipulse-agent"

if ($Source -eq "git") {
  Require-Command git
  git clone --depth 1 --branch $Version https://github.com/masbenx/omnipulse-agent.git $repoDir | Out-Null
} elseif ($Source -eq "zip") {
  $archive = Join-Path $workDir "omnipulse-agent.zip"
  $ref = "heads"
  if ($Version.StartsWith("v")) { $ref = "tags" }
  $url = "https://github.com/masbenx/omnipulse-agent/archive/refs/$ref/$Version.zip"
  Invoke-WebRequest -Uri $url -OutFile $archive
  Expand-Archive -Path $archive -DestinationPath $workDir
  $extracted = Get-ChildItem -Path $workDir -Directory | Where-Object { $_.Name -like "omnipulse-agent-*" } | Select-Object -First 1
  if (-not $extracted) { throw "unable to find extracted folder" }
  Move-Item $extracted.FullName $repoDir
} else {
  throw "invalid source: $Source"
}

Push-Location $repoDir
$env:CGO_ENABLED = "0"
go build -o (Join-Path $workDir "omnipulse-agent.exe") .
Pop-Location

New-Item -ItemType Directory -Path $Prefix -Force | Out-Null
Copy-Item -Path (Join-Path $workDir "omnipulse-agent.exe") -Destination (Join-Path $Prefix "omnipulse-agent.exe") -Force

Write-Output "installed to $Prefix\omnipulse-agent.exe"
