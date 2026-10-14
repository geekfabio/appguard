param(
  [string]$InstallDir = "C:\Program Files\AppGuard",
  [string]$ConfigSource = ".\appguard.json",
  [int]$Port = 8443
)

$ErrorActionPreference = "Stop"
$ServiceName = "AppGuard"
$FirewallRuleName = "AppGuard Controller"
$SourceExe = ".\appguard.exe"
$TargetExe = Join-Path $InstallDir "appguard.exe"
$TargetConfig = Join-Path $InstallDir "appguard.json"

function Test-Administrator {
  $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
  $principal = New-Object Security.Principal.WindowsPrincipal($identity)
  return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

if (-not (Test-Administrator)) {
  throw "Execute este script como Administrador."
}

if (-not (Test-Path $SourceExe)) {
  throw "Nao foi encontrado: $SourceExe"
}

$hasConfig = Test-Path $ConfigSource
$config = $null

if ($hasConfig) {
  try {
    $config = Get-Content $ConfigSource -Raw | ConvertFrom-Json
  } catch {
    throw "JSON de configuracao invalido: $($_.Exception.Message)"
  }

  if ($config.role -ne "controller") {
    throw "Este instalador e destinado ao Controller. O campo role deve ser controller."
  }
}

Write-Host "A instalar AppGuard Controller..." -ForegroundColor Cyan
if (-not $hasConfig) {
  Write-Host "Nenhum JSON inicial encontrado. O wizard visual sera aberto."
}
Write-Host "Destino: $InstallDir"
Write-Host "Porta do dashboard/API: $Port"

$existingService = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue

if ($null -ne $existingService) {
  Write-Host "Foi encontrada uma instalacao existente. A parar o servico..."

  if ($existingService.Status -ne "Stopped") {
    Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue

    $timeout = 20
    while ($timeout -gt 0) {
      $service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
      if ($null -eq $service -or $service.Status -eq "Stopped") {
        break
      }

      Start-Sleep -Seconds 1
      $timeout--
    }
  }

  Write-Host "A remover o servico antigo..."
  sc.exe delete $ServiceName | Out-Null

  $timeout = 20
  while ($timeout -gt 0) {
    if ($null -eq (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue)) {
      break
    }

    Start-Sleep -Seconds 1
    $timeout--
  }
}

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

Copy-Item $SourceExe $TargetExe -Force

if ($hasConfig) {
  Copy-Item $ConfigSource $TargetConfig -Force
} else {
  Write-Host "A abrir configuracao visual do AppGuard Controller..."
  Push-Location $InstallDir
  try {
    .\appguard.exe setup -config $TargetConfig
  } finally {
    Pop-Location
  }

  if (-not (Test-Path $TargetConfig)) {
    throw "A configuracao nao foi criada. Instalacao cancelada."
  }

  try {
    $config = Get-Content $TargetConfig -Raw | ConvertFrom-Json
  } catch {
    throw "O JSON criado pelo wizard e invalido: $($_.Exception.Message)"
  }

  if ($config.role -ne "controller") {
    throw "O wizard nao foi concluido como Controller."
  }
}

Push-Location $InstallDir
try {
  Write-Host "A instalar Windows Service..."
  .\appguard.exe install -config $TargetConfig

  sc.exe config $ServiceName start= auto | Out-Null
  sc.exe failure $ServiceName reset= 3600 actions= restart/5000/restart/15000/restart/60000 | Out-Null
  sc.exe failureflag $ServiceName 1 | Out-Null

  $existingFirewall = Get-NetFirewallRule -DisplayName $FirewallRuleName -ErrorAction SilentlyContinue
  if ($null -eq $existingFirewall) {
    Write-Host "A criar regra de Firewall para TCP $Port..."
    New-NetFirewallRule -DisplayName $FirewallRuleName -Direction Inbound -Protocol TCP -LocalPort $Port -Action Allow | Out-Null
  } else {
    Write-Host "Regra de Firewall ja existe."
  }

  Write-Host "A iniciar AppGuard Controller..."
  .\appguard.exe start
} finally {
  Pop-Location
}

Start-Sleep -Seconds 3

$service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($null -eq $service) {
  throw "O servico AppGuard nao foi criado."
}

if ($service.Status -ne "Running") {
  Write-Warning "O servico foi instalado, mas o estado actual e: $($service.Status)"
} else {
  Write-Host "AppGuard Controller instalado e em execucao." -ForegroundColor Green
}

$healthUrl = "http://localhost:$Port/healthz"

try {
  $health = Invoke-RestMethod -Uri $healthUrl -Method Get -TimeoutSec 5
  Write-Host "Health check: $($health.status)" -ForegroundColor Green
} catch {
  Write-Warning "Nao foi possivel validar $healthUrl : $($_.Exception.Message)"
}

Write-Host ""
Write-Host "Configuracao: $TargetConfig"
Write-Host "Dashboard local: http://localhost:$Port"
Write-Host "Dashboard na LAN: http://IP-DESTE-PC:$Port"
Write-Host "O Controller tambem funciona como Agent local e supervisiona as apps deste PC."
Write-Host "Nao e necessario executar enroll."
