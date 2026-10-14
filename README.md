# AppGuard

MVP em Go para Windows Server/Windows Client destinado a manter processos em segundo plano activos e permitir controlo centralizado na LAN.

## Funcionalidades

- Executa como Windows Service.
- Arranca automaticamente com o Windows.
- Monitoriza múltiplos executáveis.
- Reinicia processos que terminem inesperadamente.
- Retry progressivo: 5s, 10s, 30s, 60s e 120s.
- Guarda `enabled/disabled` no ficheiro local.
- Um único binário pode funcionar como agente ou controlador.
- Heartbeat dos agentes.
- Comandos de broadcast:
  - `disable-all`
  - `enable-all`
  - `stop-all`
  - `restart-all`

## Compilar

No Windows:

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

## Configuração

```powershell
Copy-Item appguard.example.json appguard.json
```

Em cada PC, definir um `agentId` único e o endereço do PC controlador.

## PC controlador

```powershell
.\appguard.exe controller -config .\appguard.json
```

Consultar agentes:

```powershell
$headers = @{ Authorization = "Bearer CHANGE-THIS-LONG-RANDOM-TOKEN" }
Invoke-RestMethod http://127.0.0.1:8443/api/v1/admin/agents -Headers $headers
```

Desactivar supervisão em todos:

```powershell
.\scripts\control.ps1 -Action disable-all -Controller http://127.0.0.1:8443 -Token "CHANGE-THIS-LONG-RANDOM-TOKEN"
```

Parar imediatamente todos os processos supervisionados:

```powershell
.\scripts\control.ps1 -Action stop-all -Controller http://127.0.0.1:8443 -Token "CHANGE-THIS-LONG-RANDOM-TOKEN"
```

## Agentes

```powershell
.\scripts\install.ps1 -ConfigSource .\appguard.json
```

Ou manualmente:

```powershell
.\appguard.exe install -config "C:\Program Files\AppGuard\appguard.json"
.\appguard.exe start
```

## Semântica dos comandos

- `disable-all`: impede novas recuperações automáticas; não mata imediatamente processos já activos.
- `enable-all`: volta a activar a supervisão.
- `stop-all`: termina imediatamente todos os processos supervisionados.
- `restart-all`: reinicia todos os processos supervisionados.

## Segurança

Este MVP usa um token Bearer partilhado. Para produção:

1. usar HTTPS/TLS;
2. gerar tokens diferentes por agente;
3. limitar o firewall à sub-rede;
4. assinar digitalmente o executável;
5. adicionar logs de auditoria;
6. não permitir execução remota de PowerShell/cmd nem caminhos arbitrários;
7. implementar rotação de credenciais;
8. criar confirmação adicional para comandos globais.

## Próximos passos

- Dashboard web no próprio binário Go.
- Comandos por computador e grupos.
- Persistência SQLite no controlador.
- Histórico de uptime, falhas e retries.
- Actualização segura do agente.
- Descoberta/registo automático de computadores.
- TLS interno e autenticação por dispositivo.
- Instalador MSI/EXE.
