# AppGuard v1.1.1

This release removes the shared token requirement.

## Changes

- Controller API no longer requires a Bearer token.
- Agents no longer send an Authorization header.
- First-run setup no longer asks for a token.
- JSON configuration examples no longer contain a token.
- The dashboard no longer asks for a token.
- Enrollment works without authentication.
- Controller and Agent communication remains limited to AppGuard's structured endpoints and commands.

## Security model

AppGuard v1.1.1 is intended for trusted LAN environments.

Because the API is now unauthenticated:

- restrict TCP port 8443 to the internal subnet using Windows Firewall;
- do not expose the Controller directly to the public Internet;
- use a VPN or network-level access control for remote administration.

The application still does not expose arbitrary PowerShell, CMD, or shell execution.

## First-run setup

Run `appguard.exe` without an `appguard.json` and the visual setup wizard opens automatically. Choose Controller or Agent and select the application executable with the native Windows picker.
