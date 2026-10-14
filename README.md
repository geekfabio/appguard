# AppGuard

> **Versão actual: v0.3.0**

AppGuard é um supervisor de processos Windows escrito em **Go**, com watchdog local e controlo centralizado de múltiplos computadores através da LAN.

## Novidades da v0.3.0

- persistência SQLite no controlador;
- computadores mantidos após reinício do controlador;
- histórico persistente de eventos;
- registo de crashes dos processos;
- histórico de retries;
- eventos de reinício bem-sucedido/falhado;
- contador de retries por processo;
- página individual por computador;
- base de dados configurável através de `controller.databasePath`;
- dashboard actualizado para mostrar falhas recentes;
- compatibilidade com a configuração da v0.2.

O controlador utiliza `modernc.org/sqlite`, uma implementação SQLite em Go que não requer CGO.

## Arquitectura

```text
                    APPGUARD CONTROLLER
                   Windows / PC Central
                  http://IP:8443
                         │
                    appguard.db
                         │
             ┌───────────┼───────────┐
             │           │           │
             ▼           ▼           ▼
          PC-001      PC-002      PC-003
          Agent       Agent       Agent
             │           │           │
             ▼           ▼           ▼
          app.exe      app.exe      app.exe
```

Cada agente continua a supervisionar localmente os seus processos mesmo quando o controlador está desligado.

## Dashboard

Iniciar:

```powershell
.\appguard.exe controller -config .\appguard.json
```

Abrir:

```text
http://192.168.1.10:8443
```

O dashboard apresenta:

- total de computadores;
- Online/Offline;
- processos activos;
- falhas recentes;
- retries;
- grupos;
- última comunicação;
- histórico persistente;
- acções por computador.

Clicar no nome de um computador abre a respectiva página de detalhe.

## SQLite

Por defeito:

```json
{
  "controller": {
    "databasePath": "appguard.db"
  }
}
```

A base de dados contém actualmente:

```text
agents
events
```

São persistidos:

- último heartbeat;
- grupo do agente;
- estado dos processos;
- crashes;
- retries;
- falhas de restart;
- restarts bem sucedidos;
- comandos administrativos;
- reconexões.

## Eventos enviados pelos agentes

```text
process-crash
process-check-failed
retry-scheduled
retry-limit-reached
restart-success
restart-failed
```

## Política de recuperação

```text
Retry 1 → 5s
Retry 2 → 10s
Retry 3 → 30s
Retry 4 → 60s
Retry 5 → 120s
```

## Configuração

```json
{
  "agentId": "PC-001",
  "group": "Loja-01",
  "enabled": true,
  "checkEverySeconds": 10,
  "controller": {
    "url": "http://192.168.1.10:8443",
    "token": "ALTERAR-PARA-UM-TOKEN-SEGURO",
    "pollEverySeconds": 5,
    "listen": ":8443",
    "databasePath": "appguard.db"
  },
  "applications": [
    {
      "name": "ProcessoPrincipal",
      "path": "C:\\Apps\\ProcessoPrincipal\\processo.exe",
      "workingDirectory": "C:\\Apps\\ProcessoPrincipal",
      "enabled": true,
      "autoRestart": true,
      "maxRetries": 5,
      "resetAfterSeconds": 300
    }
  ]
}
```

## Compilar

```powershell
go mod tidy
go build -o appguard.exe ./cmd/appguard
```

## Instalar como serviço

Executar como Administrador:

```powershell
.\scripts\install.ps1 -ConfigSource .\appguard.json
```

Ou:

```powershell
.\appguard.exe install -config "C:\Program Files\AppGuard\appguard.json"
.\appguard.exe start
```

## Comandos

```text
enable-all
disable-all
restart-all
stop-all
```

Podem ser aplicados globalmente, por grupo ou por máquina.

## API

```text
POST /api/v1/heartbeat
POST /api/v1/agent-events
GET  /api/v1/commands/{agentId}

GET  /api/v1/admin/agents
GET  /api/v1/admin/events
GET  /api/v1/admin/agent-detail/{agentId}

POST /api/v1/admin/broadcast/{action}
POST /api/v1/admin/agent/{agentId}/{action}
POST /api/v1/admin/group/{group}/{action}
```

## Segurança

A v0.3.0 continua direccionada sobretudo a redes LAN controladas.

Antes de exposição fora da rede interna:

1. HTTPS/TLS;
2. autenticação individual dos agentes;
3. tokens rotativos;
4. firewall;
5. autenticação real no dashboard;
6. assinatura do binário;
7. RBAC;
8. auditoria completa.

O AppGuard não implementa execução remota arbitrária de PowerShell/CMD.

## Estrutura

```text
appguard/
├── cmd/appguard/
├── internal/
│   ├── config/
│   ├── control/
│   ├── process/
│   ├── service/
│   ├── storage/
│   └── watchdog/
├── scripts/
├── appguard.example.json
├── VERSION
├── go.mod
└── README.md
```

## Roadmap

### v0.4

- gestão visual de grupos;
- gestão remota das aplicações;
- filtros e pesquisa;
- estatísticas de uptime;
- retenção configurável de eventos;
- comandos persistentes;
- configuração remota.

### v0.5

- instalador MSI/EXE;
- enrollment automático;
- auto-update;
- versionamento de agentes;
- distribuição de configurações;
- TLS interno.

### v1.0

- autenticação e RBAC;
- políticas por grupo;
- auditoria completa;
- actualizações assinadas;
- operação em dezenas/centenas de máquinas;
- alta disponibilidade opcional.

## Versão

```text
AppGuard v0.3.0
```
