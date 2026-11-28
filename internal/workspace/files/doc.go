// Package files is the workspace's filesystem backend (ADR 0010). The
// gRPC workspace server (internal/workspace/server) wraps FS to expose
// Read, Write, and List to a caller. Paths are confined to
// the workspace root; per ADR 0010 this confinement is convenience,
// not a trust boundary.
package files
