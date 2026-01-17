param(
  [ValidateSet("git", "zip")] [string]$Source = "git",
  [string]$Version = "main",
  [string]$Prefix = "C:\Program Files\OmniPulse"
)

$ErrorActionPreference = "Stop"

function Require-Command($name) {
  if (-not (Get-Command $name -ErrorAction SilentlyContinue)) {
    throw "$name is required"
  }
}

Require-Command go

$workDir = New-Item -ItemType Directory -Path ([System.IO.Path]::Combine([System.IO.Path]::GetTempPath(), "omnipulse-agent" + [System.Guid]::NewGuid().ToString("N")))
$repoDir = Join-Path $workDir "omnipulse-agent"

if ($Source -eq "git") {
  Require-Command git
  git clone --depth 1 --branch $Version https://github.com/masbenx/omnipulse-agent.git $repoDir | Out-Null
} elseif ($Source -eq "zip") {
  $archive = Join-Path $workDir "omnipulse-agent.zip"
  $url = "https://github.com/masbenx/omnipulse-agent/archive/refs/heads/$Version.zip"
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
