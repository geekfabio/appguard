param(
  [string]$InstallDir = "C:\Program Files\AppGuard",
  [string]$ConfigSource = ".\appguard.json"
)

$ErrorActionPreference = "Stop"

if (-not ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()
).IsInRole([Security.Principal.WindowsBuiltInRole] "Administrator")) {
  throw "Execute este script como Administrador."
}

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Copy-Item ".\appguard.exe" "$InstallDir\appguard.exe" -Force
Copy-Item $ConfigSource "$InstallDir\appguard.json" -Force

Push-Location $InstallDir
try {
  .\appguard.exe install -config "$InstallDir\appguard.json"
  sc.exe failure AppGuard reset= 3600 actions= restart/5000/restart/15000/restart/60000 | Out-Null
  .\appguard.exe start
} finally {
  Pop-Location
}

Write-Host "AppGuard instalado em $InstallDir"
