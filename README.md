# AppGuard

AppGuard é um supervisor de processos Windows escrito em Go, com controlo centralizado pela rede local.

## v0.2

A v0.2 acrescenta:

- dashboard web embutido no mesmo binário;
- estado Online/Offline por computador;
- grupos de máquinas;
- comandos globais;
- comandos por computador;
- histórico de eventos do controlador;
- contagem de processos activos;
- polling automático do dashboard;
- autenticação Bearer nas APIs administrativas.

## Arquitectura

Cada computador executa o AppGuard como Windows Service. O agente mantém os processos configurados activos e comunica periodicamente com um controlador central.

O controlador pode ser executado num único PC da LAN:

\`\`\`powershell
.\appguard.exe controller -config .\appguard.json
\`\`\`

Abrir no navegador:

\`\`\`text
http://IP-DO-CONTROLADOR:8443
\`\`\`

O dashboard pede o token Bearer configurado em \`controller.token\`.

## Funcionalidades do agente

- arranque automático com o Windows;
- supervisão de múltiplos executáveis;
- retry progressivo: 5s, 10s, 30s, 60s e 120s;
- configuração local persistente;
- execução sem sessão interactiva;
- heartbeat para o controlador;
- recepção de comandos remotos.

## Comandos remotos

- \`enable-all\`
- \`disable-all\`
- \`restart-all\`
- \`stop-all\`

Os comandos podem ser enviados globalmente ou a uma máquina específica.

## Grupos

Cada agente pode ser associado a um grupo:

\`\`\`json
{
  "agentId": "PC-001",
  "group": "Loja-01"
}
\`\`\`

Isto prepara a aplicação para comandos por grupo, por exemplo:

\`\`\`text
POST /api/v1/admin/group/Loja-01/restart-all
\`\`\`

## Compilar

\`\`\`powershell
go mod tidy
go build -o appguard.exe ./cmd/appguard
\`\`\`

## Instalar como serviço

Executar como Administrador:

\`\`\`powershell
Copy-Item appguard.example.json appguard.json
.\scripts\install.ps1 -ConfigSource .\appguard.json
\`\`\`

Ou manualmente:

\`\`\`powershell
.\appguard.exe install -config "C:\Program Files\AppGuard\appguard.json"
.\appguard.exe start
\`\`\`

## Segurança

O MVP/v0.2 usa Bearer Token partilhado. Para produção recomenda-se:

1. HTTPS/TLS;
2. token distinto por dispositivo;
3. firewall limitado à sub-rede;
4. assinatura digital do executável;
5. auditoria persistente;
6. rotação de credenciais;
7. não permitir comandos arbitrários de shell;
8. confirmação adicional para operações globais destrutivas.

## Próximos passos

- persistência SQLite para agentes, eventos e comandos;
- autenticação do dashboard com utilizadores;
- actualizações automáticas assinadas;
- instalador MSI/EXE;
- configuração remota das aplicações;
- TLS interno;
- descoberta/enrollment automático dos agentes;
- dashboard de uptime, falhas e retries.
