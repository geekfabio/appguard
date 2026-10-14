# AppGuard v1.0.1

Patch de comportamento de inicialização.

## Novidade

Foi adicionado um atraso inicial configurável por aplicação:

```json
{
  "startupDelaySeconds": 60
}
```

Por defeito, cada aplicação supervisionada aguarda **60 segundos** após o arranque do AppGuard antes da primeira tentativa de inicialização.

Durante este período:

- o AppGuard não inicia a aplicação;
- não consome retries;
- não agenda restarts;
- continua activo e pode comunicar com o controlador.

Ao terminar o atraso, se o processo não estiver a correr, o AppGuard tenta iniciá-lo imediatamente.

Depois da inicialização, as falhas passam a seguir a política normal:

```text
5s → 10s → 30s → 60s → 120s
```

O campo é configurável individualmente por aplicação e também pode ser alterado através da configuração remota estruturada.

## Controller Node

O mesmo comportamento aplica-se aos processos locais supervisionados pelo computador controlador.

## Compatibilidade

Configurações antigas sem `startupDelaySeconds` recebem automaticamente o valor padrão de **60 segundos**.
