param(
  [string]$InstallDir = "C:\Program Files\AppGuard",
  [string]$ConfigSource = ".\appguard.agent.json"
)

$ErrorActionPreference = "Stop"
$ServiceName = "AppGuard"
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

  if ($config.role -ne "agent") {
    throw "Este instalador e destinado aos Agents. O campo 'role' deve ser 'agent'."
  }

  if (-not $config.controller.url) {
    throw "A configuracao do Agent nao possui controller.url."
  }
}

Write-Host "A instalar AppGuard Agent..." -ForegroundColor Cyan
if ($hasConfig) {
  Write-Host "Controller: $($config.controller.url)"
} else {
  Write-Host "Nenhum JSON inicial encontrado. O wizard visual sera aberto."
}
Write-Host "Destino: $InstallDir"

# Se ja existir uma instalacao, parar e remover o servico primeiro.
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

  # Aguardar o Service Control Manager libertar o servico.
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
  Write-Host "A abrir configuracao visual do AppGuard Agent..."
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

  if ($config.role -ne "agent") {
    throw "O wizard nao foi concluido como Agent."
  }

  if (-not $config.controller.url) {
    throw "A configuracao criada nao possui controller.url."
  }
}

Push-Location $InstallDir
try {
  Write-Host "A instalar Windows Service..."
  .\appguard.exe install -config $TargetConfig

  # Arranque automatico e recuperacao em caso de falha.
  sc.exe config $ServiceName start= auto | Out-Null
  sc.exe failure $ServiceName reset= 3600 actions= restart/5000/restart/15000/restart/60000 | Out-Null
  sc.exe failureflag $ServiceName 1 | Out-Null

  Write-Host "A iniciar AppGuard Agent..."
  .\appguard.exe start
} finally {
  Pop-Location
}

Start-Sleep -Seconds 2

$service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($null -eq $service) {
  throw "O servico AppGuard nao foi criado."
}

if ($service.Status -ne "Running") {
  Write-Warning "O servico foi instalado, mas o estado actual e: $($service.Status)"
} else {
  Write-Host "AppGuard Agent instalado e em execucao." -ForegroundColor Green
}

Write-Host ""
Write-Host "Configuracao: $TargetConfig"
Write-Host "Agent ID: sera automaticamente o nome deste PC."
Write-Host "Controller: $($config.controller.url)"
Write-Host "Nao e necessario executar 'enroll'."
