# AppGuard v1.0.0

Primeira versão estável do AppGuard para supervisão centralizada de processos Windows em rede local.

## Principais novidades

- **Controller Node completo**: o computador controlador também funciona como agente e watchdog local.
- O controlador supervisiona os seus próprios processos e aparece no dashboard.
- Suporte explícito a `role: "controller"` e `role: "agent"`.
- Controlo individual por aplicação: activar, desactivar, parar e reiniciar.
- Comandos persistentes em SQLite com entrega a máquinas offline.
- ACK de comandos depois da tentativa de execução.
- Configuração remota estruturada de grupos, intervalo e aplicações.
- Enrollment de agentes.
- Versionamento dos agentes visível no dashboard.
- Health checks: `/healthz` e `/readyz`.
- Histórico de falhas, retries e restarts.
- Retry progressivo: 5s → 10s → 30s → 60s → 120s.
- Protecção contra múltiplos restarts concorrentes.
- Build automatizado para Windows amd64.

## Controller Node

O controlador pode agora executar simultaneamente Dashboard/API, SQLite, watchdog local, heartbeat, cliente de comandos e supervisão dos processos configurados no próprio PC.

Use `appguard.controller.example.json` com `"role": "controller"`.

## Artefactos

- `appguard.exe` — binário Windows amd64.
- `AppGuard-v1.0.0-windows-amd64.zip` — pacote com binário, configurações exemplo e scripts.

## Requisitos

- Windows 10/11 ou Windows Server.
- Privilégios de Administrador para instalar como Windows Service.
- Rede LAN entre controlador e agentes.
- Go 1.24+ apenas para compilar a partir do código-fonte.

## Instalação rápida

```powershell
.\scripts\install.ps1 -ConfigSource .\appguard.json
```

## Segurança

A v1.0 não permite execução arbitrária de PowerShell, CMD ou scripts remotos. Para exposição fora de uma LAN controlada, recomenda-se TLS/mTLS e autenticação individual por dispositivo.
