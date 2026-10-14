# AppGuard v1.1.4

This release fixes Controller and Agent visibility in the dashboard.

## Fixes

- The Controller now registers itself directly in the controller database at startup.
- The Controller appears in the dashboard immediately, even before its first local heartbeat.
- Fixed invalid dashboard JavaScript that could leave the agent table empty.
- Fixed dashboard action buttons after the frontend refactor.
- The Controller local Agent waits until the local Controller API is healthy before sending heartbeats.
- Agent configuration generation now prefers the IPv4 address used by the system's primary/default route.
- This avoids publishing Hyper-V, Docker, VPN or other virtual adapter addresses in `appguard.agent.json`.
- The dashboard now shows the actual AppGuard version.

## Expected result

When the Controller starts, its own PC should appear in the dashboard immediately.

Agents using the generated `appguard.agent.json` should appear after they can reach:

```text
http://CONTROLLER-LAN-IP:8443
```

The Agent ID continues to be the Windows computer name.
