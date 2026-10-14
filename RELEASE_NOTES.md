# AppGuard v1.1.0

This release adds a visual first-run configuration experience so AppGuard no longer requires manual JSON editing for initial setup.

## First-run wizard

When `appguard.json` is missing, running `appguard.exe` now opens a local setup interface automatically.

The interface allows you to:

- choose **Controller** or **Agent**;
- use the Windows computer name automatically as the agent ID;
- select the monitored `.exe` using a native Windows file picker;
- add multiple monitored applications;
- configure the watchdog check interval;
- configure startup delay and retry settings;
- enter the Controller URL and shared token for Agent machines;
- generate a secure token by default for a Controller.

The configuration is saved beside `appguard.exe`.

The setup interface can also be reopened manually:

```powershell
.\appguard.exe setup
```

## Controller behavior

The Controller continues to behave as an Agent on its own computer. It runs the local watchdog and checks the applications configured on the Controller itself.

If a monitored local process is not running, AppGuard starts it after the configured `startupDelaySeconds` and then uses the normal retry policy.

## Agent ID

`agentId` is now always derived from the Windows PC name using the operating system hostname.

## Application selection

The setup wizard uses the native Windows executable picker, so the user can browse to the application to monitor instead of typing its path manually.

## Compatibility

Existing JSON configuration files continue to work. On load, the current computer name overrides a manually stored `agentId` so each machine identifies itself consistently.
