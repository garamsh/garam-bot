# ADR 0039: `garam-bot daemon` is the connector of a PC and of a naru host, running several bots, and the PC app shows it

> Status: accepted
> Date: 2026-11-24

## Context

- **sauce's direction:**
  - a PC runs one daemon that runs several bots (`garamsh/discussions` discussion 6, comment 19856107);
  - the PC daemon and the PC app that shows it are designed in goal ④ and built in goal ⑤ (comment 19857097);
  - garam-bot holds the runtime, the cluster connector, the PC daemon and the PC app (comment 19857166).
- **A naru host is an execution place too** (comment 19856107). Its workspaces are naru Machines, and the Machine API's caller is garam-bot's provider at the place, registered once (naru#409).
- **One pod is one PC** (ADR 0031): one organization's bots, a process per bot. `garam-bot run` runs one Bot from a file (ADR 0033).
- **The workspace providers** are a Go driver interface that a place links (ADR 0034): container and mvmd on Linux, container on a Mac, a naru Machine on a naru host.

## Decision

1. **`garam-bot daemon` is one long-running process per place, in the root module's `garam-bot` binary.** It is the connector of a PC and of a naru host. It:
   - registers the place once (ADR 0036);
   - long-polls its declarations, and reads the place's own from a directory of files;
   - runs each Bot as a child `garam-bot run`;
   - makes and reaches each Bot's workspaces through the providers it is configured with;
   - exchanges each Bot's token (ADR 0037);
   - reports to garam.
2. **The providers by place:**
   - a Linux PC: container (Docker or Podman), and mvmd where the PC has KVM;
   - a Mac: container;
   - a naru host: the naru Machine provider.
3. **Registration on a PC goes through the PC app.**
   1. A person signs in with the device grant (RFC 8628).
   2. The app asks garam for the place's one-time token and hands it to the daemon.
   3. The daemon makes its key, which never leaves the PC: the operating system's key store where it has one, a file only the daemon's user reads otherwise.

   On a naru host, the token is given at install.
4. **The PC app is a desktop app in the module `app/`.** It reads the daemon over a local socket only the PC's user reaches, and shows the place, its bots, their workspaces and their state. Through it a person signs in, registers the place, and starts and stops bots. It holds no bot's token.
5. **The daemon runs as a system service:** systemd on Linux, launchd on a Mac. It opens no inbound port.

## Consequences

- The naru-host connector and the PC daemon are one program with different providers, so goal ④'s naru host and goal ⑤'s PC share their core.
- A PC's bots stop when the PC sleeps, and a bot that must stay up is placed on a cluster or a naru host.
- The PC app's desktop toolkit and its packaging are goal ⑤'s first decision.

## Signs the decision was wrong

- A PC's daemon and a naru host's diverge until one binary carries two programs.
- People want the PC app to run bots without the daemon, which would show the service is in the way.
- The operating system key stores cannot hold an RSA key the daemon signs with, so the file fallback becomes the rule.
