module dns-geosite-proxy

go 1.23

require (
	// DNS server/client library - the de-facto standard in Go ecosystem.
	// Used for: UDP/TCP DNS listener, DNS message parsing, upstream forwarding.
	// Docs: https://pkg.go.dev/github.com/miekg/dns
	github.com/miekg/dns v1.1.62

	// Protobuf runtime - used for manual wire-format decoding of dlc.dat.
	// We use only the protowire sub-package (no codegen required).
	// Docs: https://pkg.go.dev/google.golang.org/protobuf/encoding/protowire
	google.golang.org/protobuf v1.36.5
)

require (
	golang.org/x/mod v0.18.0 // indirect
	golang.org/x/net v0.27.0 // indirect
	golang.org/x/sync v0.7.0 // indirect
	golang.org/x/sys v0.22.0 // indirect
	golang.org/x/tools v0.22.0 // indirect
)
