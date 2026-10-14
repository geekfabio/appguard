# AppGuard

> **Versão actual: v0.4.0**

AppGuard é um supervisor de processos Windows escrito em **Go**, com watchdog local, controlo centralizado pela LAN, dashboard web embutido e persistência SQLite.

## Novidades da v0.4.0

- pesquisa de computadores por nome ou grupo;
- filtro por grupo;
- filtro Online/Offline;
- edição remota da configuração de cada agente;
- alteração remota do grupo;
- alteração remota do intervalo de verificação;
- gestão remota da lista de aplicações supervisionadas;
- aplicação da nova configuração sem reiniciar o serviço;
- persistência automática da configuração recebida no `appguard.json`;
- heartbeat passa a incluir o snapshot da configuração do agente;
- página individual por computador actualizada;
- visualização do estado `restartPending`.

> Os comandos remotos continuam em memória no controlador nesta versão. A persistência dos comandos para agentes offline passa para a v0.5.

## Arquitectura

```text
                    APPGUARD CONTROLLER
                  Dashboard + SQLite
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
       processos      processos    processos
```

Cada agente continua a supervisionar localmente os seus processos mesmo que o controlador esteja desligado.

## Dashboard

Iniciar o controlador:

```powershell
.\appguard.exe controller -config .\appguard.json
```

Abrir:

```text
http://192.168.1.10:8443
```

O dashboard permite:

- consultar computadores Online/Offline;
- pesquisar PCs;
- filtrar por grupo;
- filtrar por estado;
- consultar processos activos;
- consultar retries e falhas;
- reiniciar/parar agentes;
- abrir a página individual de cada PC.

## Configuração remota

Na página individual do computador é apresentada a configuração estruturada actual.

Exemplo:

```json
{
  "group": "Loja-01",
  "checkEverySeconds": 10,
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

Depois de guardar:

1. o controlador coloca a configuração na fila do agente;
2. o agente recebe `apply-settings`;
3. os valores são validados;
4. o watchdog actualiza a configuração em memória;
5. o ficheiro `appguard.json` é actualizado;
6. o novo intervalo passa a ser usado automaticamente.

## Segurança

A configuração remota é limitada a campos estruturados do AppGuard.

Não existe endpoint para execução arbitrária de:

- PowerShell;
- CMD;
- scripts;
- comandos shell.

Isto reduz significativamente o risco de utilização indevida do agente.

## Política de recuperação

```text
5s → 10s → 30s → 60s → 120s
```

Uma aplicação não pode ter vários restarts concorrentes. O estado `restartPending` impede tentativas duplicadas.

O contador de retries é reposto depois do período estável definido em:

```json
{
  "resetAfterSeconds": 300
}
```

## Persistência SQLite

Actualmente são persistidos:

- agentes;
- último heartbeat;
- estado dos processos;
- grupo;
- eventos;
- crashes;
- retries;
- falhas e sucessos de restart.

Base predefinida:

```json
{
  "controller": {
    "databasePath": "appguard.db"
  }
}
```

## Compilar

```powershell
go mod tidy
go build -o appguard.exe ./cmd/appguard
```

## Instalar como Windows Service

Executar como Administrador:

```powershell
.\scripts\install.ps1 -ConfigSource .\appguard.json
```

Ou manualmente:

```powershell
.\appguard.exe install -config "C:\Program Files\AppGuard\appguard.json"
.\appguard.exe start
```

## Comandos operacionais

```text
enable-all
disable-all
restart-all
stop-all
```

Podem ser aplicados:

- globalmente;
- por grupo;
- por computador.

## API v0.4

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

PUT  /api/v1/admin/agent-settings/{agentId}
```

## Estrutura

```text
appguard/
├── cmd/
│   └── appguard/
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

### v0.5

- comandos persistentes em SQLite;
- entrega de comandos depois de agentes offline voltarem;
- instalador MSI/EXE;
- enrollment automático;
- identificação/versionamento dos agentes;
- auto-update;
- TLS interno;
- autenticação por dispositivo.

### v0.6

- autenticação de utilizadores;
- RBAC;
- estatísticas de uptime;
- retenção configurável de eventos;
- relatórios;
- auditoria administrativa.

### v1.0

- hardening para produção;
- políticas avançadas por grupo;
- actualizações assinadas;
- operação em dezenas ou centenas de máquinas;
- alta disponibilidade opcional.

## Versão

```text
AppGuard v0.4.0
```
