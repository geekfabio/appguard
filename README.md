# AppGuard

> **Versão actual: v1.1.0**

AppGuard é um supervisor de processos Windows escrito em **Go**, com watchdog local, controlo centralizado pela LAN, dashboard web embutido e persistência SQLite.

## Novidades da v1.0.0

- o controlador também funciona como agente local e watchdog;
- o PC controlador aparece no dashboard e supervisiona os próprios processos;
- suporte a `role: "agent"` e `role: "controller"`;
- controlo individual por aplicação;
- comandos persistentes em SQLite com ACK;
- entrega a agentes que estavam offline;
- configuração remota estruturada;
- enrollment e versionamento dos agentes;
- endpoints `/healthz` e `/readyz`;
- CI/build Windows amd64;
- pacote de Release com binário, exemplos e scripts.

## First-run visual setup

If `appguard.json` does not exist, running `appguard.exe` opens the AppGuard setup wizard automatically in the browser.

The wizard:

- detects the Windows computer name and uses it as the `agentId`;
- asks whether the machine is a **Controller** or **Agent**;
- lets you choose the application executable with a native Windows file picker;
- supports multiple monitored applications;
- configures the startup delay, retries and working directory;
- asks for the Controller URL only when configuring an Agent;
- writes `appguard.json` next to `appguard.exe`;
- starts AppGuard immediately after saving.

You can reopen the wizard manually with:

```powershell
.\appguard.exe setup
```

The computer name is always used as the agent ID, even if an `agentId` value is manually changed in JSON.

## Controller Node

No modo `role: "controller"`, o mesmo processo executa dashboard/API, SQLite, heartbeat, cliente de comandos e watchdog local.

Use `appguard.controller.example.json`.

## Comandos persistentes

A tabela SQLite `commands` mantém as ordens emitidas pelo controlador.

Fluxo:

```text
Administrador envia comando
        ↓
Controller grava em SQLite
        ↓
PC pode estar offline
        ↓
Agente volta à rede
        ↓
GET /api/v1/commands/{agentId}
        ↓
Agente executa
        ↓
POST /api/v1/commands/ack/{commandId}
        ↓
Controller marca como entregue
```

Isto evita que uma ordem desapareça apenas porque o computador estava desligado ou temporariamente sem rede.

A entrega segue uma estratégia **at-least-once**: se o agente executar um comando mas o ACK falhar por problema de rede, o comando pode voltar a ser recebido. Por isso, as operações do AppGuard devem continuar a ser tratadas de forma idempotente sempre que possível.

## Enrollment

Depois de configurar `controller.url` e `controller.token`:

```powershell
.\appguard.exe enroll -config .\appguard.json
```

Exemplo de resposta:

```text
AppGuard enrolled: PC-001
```

O instalador PowerShell tenta efectuar o enrollment automaticamente. Se o controlador não estiver disponível, a instalação continua e o agente poderá comunicar posteriormente através do heartbeat normal.

Endpoint:

```text
POST /api/v1/enroll
```

## Versionamento

Consultar a versão instalada:

```powershell
.\appguard.exe version
```

Resultado:

```text
AppGuard 1.1.0
```

O heartbeat inclui a versão do agente e o dashboard apresenta essa informação por computador.

## Build automático

Existe um workflow:

```text
.github/workflows/build-windows.yml
```

O pipeline executa:

1. checkout;
2. configuração do Go 1.26;
3. download das dependências;
4. `go test ./...`;
5. compilação para Windows amd64;
6. geração de `dist/appguard.exe`;
7. inclusão da configuração exemplo e scripts;
8. upload do artefacto do workflow.

O workflow é executado em pushes para `main`, pull requests e tags `v*`.

## Arquitectura

```text
                    APPGUARD CONTROLLER
                  Dashboard + SQLite
                  http://IP:8443
                         │
                    appguard.db
                         │
        agentes + eventos + comandos pendentes
                         │
             ┌───────────┼───────────┐
             │           │           │
             ▼           ▼           ▼
          PC-001      PC-002      PC-003
          v0.5.0      v0.5.0      v0.4.0
             │           │           │
             ▼           ▼           ▼
        processos     processos    processos
```

## Configuração remota

A configuração estruturada continua disponível na página individual de cada agente.

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

A configuração remota também utiliza a fila de comandos persistentes.

## Segurança

A v0.5.0 continua destinada principalmente a uma LAN controlada.

Actualmente:

- APIs protegidas por Bearer Token;
- configuração remota apenas estruturada;
- sem execução arbitrária de PowerShell/CMD;
- sem execução remota de scripts;
- comandos persistidos localmente no SQLite.

Ainda não estão implementados:

- TLS interno automático;
- autenticação individual por dispositivo;
- assinatura/verificação criptográfica de actualizações;
- RBAC.

Por isso, o auto-update completo foi deliberadamente adiado até existir validação criptográfica da versão distribuída.

## Atraso inicial por aplicação

Cada aplicação pode esperar antes da primeira inicialização através de:

```json
{
  "startupDelaySeconds": 60
}
```

Se o campo não estiver presente, o valor padrão é **60 segundos**.

Durante o atraso inicial o AppGuard não consome retries nem agenda reinícios. Ao terminar o período, se o processo não estiver a correr, ele é iniciado imediatamente. A partir daí, eventuais falhas utilizam a política normal de recuperação.

O mesmo comportamento aplica-se aos processos locais do computador controlador.

## Política de recuperação

```text
5s → 10s → 30s → 60s → 120s
```

O estado `restartPending` impede múltiplos restarts concorrentes.

## Compilar

```powershell
go mod tidy
go build -o appguard.exe ./cmd/appguard
```

## Instalar

Executar como Administrador:

```powershell
.\scripts\install.ps1 -ConfigSource .\appguard.json
```

Ou manualmente:

```powershell
.\appguard.exe install -config "C:\Program Files\AppGuard\appguard.json"
.\appguard.exe enroll -config "C:\Program Files\AppGuard\appguard.json"
.\appguard.exe start
```

## Comandos CLI

```text
install
uninstall
start
stop
status
version
enroll
controller
run
```

## API v1.0

```text
POST /api/v1/enroll
POST /api/v1/heartbeat
POST /api/v1/agent-events

GET  /api/v1/commands/{agentId}
POST /api/v1/commands/ack/{commandId}

GET  /api/v1/admin/agents
GET  /api/v1/admin/events
GET  /api/v1/admin/agent-detail/{agentId}

POST /api/v1/admin/broadcast/{action}
POST /api/v1/admin/agent/{agentId}/{action}
POST /api/v1/admin/group/{group}/{action}

POST /api/v1/admin/agent-app/{agentId}/{appName}/{action}
PUT  /api/v1/admin/agent-settings/{agentId}

GET  /healthz
GET  /readyz
```

## SQLite

Tabelas principais:

```text
agents
events
commands
```

A tabela `commands` armazena:

- ID;
- agente;
- acção;
- aplicação;
- payload;
- data de criação;
- data de confirmação/entrega.

## Roadmap

### v0.6

- autenticação individual por dispositivo;
- enrollment com token dedicado;
- TLS interno;
- manifesto de actualização;
- hash SHA-256 do binário;
- assinatura/verificação de releases;
- actualização controlada dos agentes.

### v0.7

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
- gestão de dezenas ou centenas de máquinas;
- alta disponibilidade opcional.

## Versão

```text
AppGuard v1.1.0
```
