// Command conformanceimage is the entrypoint and the commands of the conformance image of interface
// 3 (api/workspace/v1alpha1/conformance.md): linked as true, false and cat it behaves as they do,
// and run as itself it echoes every byte it reads on TCP port 7. It is built for the suite only,
// into an image no release carries (build/conformance.Dockerfile).
package main

import (
	"io"
	"net"
	"os"
	"path/filepath"
)

func main() {
	switch filepath.Base(os.Args[0]) {
	case "true":
		os.Exit(0)
	case "false":
		os.Exit(1)
	case "cat":
		os.Exit(cat(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	listener, err := net.Listen("tcp", ":7")
	if err != nil {
		os.Stderr.WriteString("listen: " + err.Error() + "\n")
		os.Exit(1)
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			os.Exit(1)
		}
		go func() {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}()
	}
}
