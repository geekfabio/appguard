# AppGuard

> **Versão actual: v0.2.0**

AppGuard é um supervisor de processos Windows escrito em **Go**, criado para manter aplicações em segundo plano sempre activas e permitir o controlo centralizado de vários computadores dentro da mesma rede local.

Foi pensado para ambientes com vários PCs/Windows Server onde uma aplicação precisa:

- iniciar automaticamente com o Windows;
- voltar a iniciar quando falha;
- aplicar tentativas de recuperação progressivas;
- funcionar mesmo sem utilizador autenticado;
- ser monitorizada a partir de um único computador;
- permitir parar, reiniciar ou desactivar a supervisão remotamente.

---

## Estado da versão

### v0.2.0

Esta versão inclui:

- Windows Service nativo;
- supervisão de múltiplos processos;
- reinício automático;
- retries progressivos;
- configuração persistente em JSON;
- agente autónomo;
- controlador central na LAN;
- heartbeat dos agentes;
- detecção Online/Offline;
- dashboard web embutido;
- grupos de computadores;
- comandos globais;
- comandos por computador;
- comandos por grupo via API;
- histórico básico de eventos;
- autenticação Bearer Token nas APIs administrativas.

---

## Arquitectura

Cada computador executa o mesmo binário `appguard.exe`.

O executável pode funcionar em dois modos:

### Agent

Instalado como Windows Service em cada computador.

Responsabilidades:

- iniciar com o Windows;
- supervisionar aplicações configuradas;
- detectar processos encerrados;
- reiniciar aplicações;
- gerir retries;
- enviar heartbeat;
- receber comandos do controlador.

### Controller

Executado num computador central da LAN.

Responsabilidades:

- receber heartbeats;
- apresentar computadores Online/Offline;
- receber estado dos processos;
- enviar comandos aos agentes;
- gerir grupos;
- manter eventos recentes;
- servir o dashboard web.

Fluxo:

```text
                    APPGUARD CONTROLLER
                   PC Administrador / LAN
                  http://IP:8443
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

Os agentes continuam a funcionar localmente mesmo quando o controlador está desligado.

---

## Dashboard

Executar o controlador:

```powershell
.\appguard.exe controller -config .\appguard.json
```

Depois abrir:

```text
http://IP-DO-CONTROLADOR:8443
```

Exemplo:

```text
http://192.168.1.10:8443
```

O dashboard mostra:

- total de computadores;
- computadores Online;
- computadores Offline;
- total de processos activos;
- grupo de cada computador;
- última comunicação;
- eventos recentes;
- estado dos processos;
- acções rápidas por máquina.

---

## Comandos disponíveis

### Global

```text
enable-all
disable-all
restart-all
stop-all
```

### Comportamento

| Comando | Resultado |
|---|---|
| `enable-all` | Reactiva a supervisão automática |
| `disable-all` | Suspende o watchdog sem terminar imediatamente os processos |
| `restart-all` | Reinicia todos os processos configurados |
| `stop-all` | Termina imediatamente os processos supervisionados |

O próprio serviço AppGuard permanece activo para poder receber novos comandos.

---

## Controlo por computador

Exemplo:

```text
POST /api/v1/admin/agent/PC-001/restart-all
```

Outro exemplo:

```text
POST /api/v1/admin/agent/PC-001/stop-all
```

---

## Grupos

Cada computador pode pertencer a um grupo.

Exemplo:

```json
{
  "agentId": "PC-001",
  "group": "Loja-01"
}
```

Outros exemplos:

```text
Loja-01
Loja-02
Administracao
Armazem
Servidores
```

Comando por grupo:

```text
POST /api/v1/admin/group/Loja-01/restart-all
```

Desta forma é possível reiniciar ou parar apenas os computadores de uma determinada área.

---

## Configuração

Criar o ficheiro:

```powershell
Copy-Item appguard.example.json appguard.json
```

Exemplo:

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
    "listen": ":8443"
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

---

## Política de recuperação

Quando um processo falha, o AppGuard aplica delays progressivos:

```text
Tentativa 1 → 5 segundos
Tentativa 2 → 10 segundos
Tentativa 3 → 30 segundos
Tentativa 4 → 60 segundos
Tentativa 5 → 120 segundos
```

Isto evita ciclos agressivos de reinício quando uma aplicação está permanentemente com erro.

---

## Compilar

Requisitos:

- Go 1.23+
- Windows amd64

Executar:

```powershell
go mod tidy
go build -o appguard.exe ./cmd/appguard
```

Cross-compile:

```powershell
$env:GOOS="windows"
$env:GOARCH="amd64"

go build -o appguard.exe ./cmd/appguard
```

---

## Instalação como Windows Service

Executar PowerShell como Administrador:

```powershell
.\scripts\install.ps1 -ConfigSource .\appguard.json
```

Ou manualmente:

```powershell
.\appguard.exe install -config "C:\Program Files\AppGuard\appguard.json"
.\appguard.exe start
```

Estado:

```powershell
.\appguard.exe status
```

Parar o serviço:

```powershell
.\appguard.exe stop
```

Remover:

```powershell
.\appguard.exe uninstall
```

---

## Controlo via PowerShell

Existe um script para enviar comandos globais:

```powershell
.\scripts\control.ps1 `
  -Action restart-all `
  -Controller http://192.168.1.10:8443 `
  -Token "TOKEN-SEGURO"
```

Parar todas as aplicações:

```powershell
.\scripts\control.ps1 `
  -Action stop-all `
  -Controller http://192.168.1.10:8443 `
  -Token "TOKEN-SEGURO"
```

---

## API

### Heartbeat

```text
POST /api/v1/heartbeat
```

### Consultar agentes

```text
GET /api/v1/admin/agents
```

### Eventos

```text
GET /api/v1/admin/events
```

### Comando global

```text
POST /api/v1/admin/broadcast/{action}
```

### Comando por agente

```text
POST /api/v1/admin/agent/{agentId}/{action}
```

### Comando por grupo

```text
POST /api/v1/admin/group/{group}/{action}
```

---

## Segurança

A versão v0.2.0 ainda é destinada principalmente a ambientes LAN controlados.

Actualmente utiliza Bearer Token.

Para produção recomenda-se:

1. HTTPS/TLS;
2. token diferente por agente;
3. firewall limitado à sub-rede interna;
4. assinatura digital do executável;
5. rotação de credenciais;
6. logs de auditoria persistentes;
7. autenticação de utilizadores no dashboard;
8. confirmação adicional para operações destrutivas;
9. não disponibilizar execução arbitrária de PowerShell, CMD ou shell;
10. validar e limitar previamente os executáveis que podem ser controlados.

---

## Estrutura

```text
appguard/
├── cmd/
│   └── appguard/
│       └── main.go
│
├── internal/
│   ├── config/
│   │   └── config.go
│   ├── control/
│   │   ├── agent.go
│   │   ├── controller.go
│   │   └── protocol.go
│   ├── process/
│   │   ├── process_windows.go
│   │   └── process_other.go
│   ├── service/
│   │   ├── service_windows.go
│   │   └── service_other.go
│   └── watchdog/
│       └── watchdog.go
│
├── scripts/
│   ├── install.ps1
│   └── control.ps1
│
├── appguard.example.json
├── VERSION
├── go.mod
└── README.md
```

---

## Roadmap

### v0.3

Planeado:

- SQLite no controlador;
- persistência de computadores;
- histórico de uptime;
- histórico de crashes;
- histórico de retries;
- página individual por computador;
- gestão visual de grupos;
- gestão remota das aplicações;
- dashboard de erros;
- filtros e pesquisa.

### v0.4

Planeado:

- instalador MSI/EXE;
- enrollment automático;
- actualização automática dos agentes;
- versionamento dos agentes;
- distribuição de configuração;
- TLS interno;
- autenticação por dispositivo.

### v1.0

Objectivo:

- solução estável para produção;
- dashboard completo;
- gestão central de dezenas ou centenas de máquinas;
- auditoria;
- RBAC;
- actualizações assinadas;
- políticas por grupo;
- alta disponibilidade opcional.

---

## Licença

A licença ainda não foi definida.

---

## Versão

```text
AppGuard v0.2.0
```
