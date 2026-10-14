# AppGuard v1.1.3

This release makes Controller setup automatically generate a ready-to-distribute Agent configuration.

## Controller setup

After saving a Controller configuration, AppGuard generates:

```text
appguard.agent.json
```

beside the Controller's own `appguard.json`.

The generated file includes:

- Agent role;
- automatically detected Controller LAN address;
- Controller port 8443;
- polling/check defaults;
- monitored application definitions;
- no token requirement.

## Agent deployment

Copy `appguard.agent.json` to an Agent machine, rename it to `appguard.json`, and place it beside `appguard.exe`.

The Agent ID is automatically replaced with that machine's Windows computer name.

If the local executable path differs on the Agent machine, run:

```powershell
.\appguard.exe setup
```

and select the correct executable using the native Windows picker.

## Existing behavior

The Controller continues to operate as its own Agent/watchdog and monitor local applications.
