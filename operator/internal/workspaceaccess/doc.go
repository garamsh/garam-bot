// Package workspaceaccess serves interface 7, workspace access, to the bots' processes of the watched
// namespaces. It resolves each request's Bot and workload instance from its credential, pins the
// instance's key, and acquires, writes to, dials and obtains session tokens for the Bot's own workspaces
// through interface 3's providers (garam-bot ADR 0043, ADR 0099).
package workspaceaccess
