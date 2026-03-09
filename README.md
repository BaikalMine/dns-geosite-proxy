# dns-geosite-proxy

DNS proxy with geosite-based routing and MikroTik firewall address-list integration.

Classifies DNS queries using [v2fly/domain-list-community](https://github.com/v2fly/domain-list-community) (`dlc.dat`),
forwards them to the appropriate upstream, and pushes resolved IPs into MikroTik
`ip/firewall/address-list` or `ipv6/firewall/address-list` via the REST API.

Designed to run as a container on MikroTik RouterOS 7.x (tested on hAP ax3, arm64).

## How it works

```
DNS client
  -> MikroTik embedded DNS (cache layer)
    -> dns-geosite-proxy :53
      -> classify domain by geosite rules
      -> forward to upstream (Yandex / Cloudflare DoH / local)
      -> return response to MikroTik
      -> push resolved IPs to MikroTik address-list (async)
```

MikroTik firewall mangle rules use the populated address-lists to mark
routing for VPN or direct paths.

## Requirements

- Go 1.23+
- Docker with buildx (for arm64 image build)
- MikroTik RouterOS 7.4+ with container support enabled
- USB flash drive on the MikroTik (recommended, 128 MB internal flash is tight)

## Quick start

```bash
# 1. Clone and enter the project
git clone https://github.com/yourname/dns-geosite-proxy
cd dns-geosite-proxy

# 2. Download geosite database
make download-dlc

# 3. Create your config
cp config.example.json config.json
# edit config.json: set mikrotik.address, username, password

# 4. Build Docker image for arm64 and save as .tar.gz
make docker-save
# -> build/dns-geosite-proxy-arm64.tar.gz
```

## Deploy to MikroTik

```bash
# Upload image to MikroTik (USB drive mounted as usb1)
scp build/dns-geosite-proxy-arm64.tar.gz admin@192.168.88.1:/usb1/
scp config.json admin@192.168.88.1:/usb1/
```

On the router (Winbox terminal or SSH):

```routeros
# Create container network
/interface/veth/add name=veth-dns address=172.20.0.2/24 gateway=172.20.0.1
/ip/address/add address=172.20.0.1/24 interface=veth-dns

# Import image
/container/add file=usb1/dns-geosite-proxy-arm64.tar.gz \
    interface=veth-dns \
    envlist=dns-proxy \
    mounts=dns-data,dns-config

# Define volume mounts
/container/mounts/add name=dns-data src=/usb1/data dst=/data
/container/mounts/add name=dns-config src=/usb1 dst=/etc/dns-proxy

# Start container
/container/start [find name=dns-geosite-proxy]

# Point MikroTik DNS to the container
/ip/dns/set servers=172.20.0.2
```

Create a restricted API user for the container:

```routeros
/user/add name=dns-proxy group=api password=<secret> address=172.20.0.2
```

## Configuration

See `config.example.json` for a fully annotated example. Key sections:

### dns.servers

Rules evaluated top-to-bottom; first match wins. The server with `"fallback": true`
catches everything not matched by earlier rules.

Rule prefix syntax (same as xray/v2ray):

| Prefix | Example | Match |
|---|---|---|
| `geosite:` | `geosite:category-ru` | dlc.dat category lookup |
| `full:` | `full:example.com` | exact FQDN only |
| `domain:` | `domain:example.com` | domain + subdomains |
| `keyword:` | `keyword:tracker` | substring anywhere |
| `regexp:` | `regexp:.*\.ru$` | Go regexp |
| _(none)_ | `example.com` | same as `domain:` |

Tags `direct`, `proxy`, `block` are built-in conventions.
`block` returns NXDOMAIN without querying any upstream.

### mikrotik.address_lists

Maps routing tags to MikroTik address-list names with TTL policy:

```json
"proxy": {
  "list": "vpn_routes",
  "ttl": "336h",
  "refresh": "72h"
}
```

An IP is added with `ttl` on first resolution. On subsequent resolutions
the timeout is refreshed only if the remaining time is below `refresh`.
This avoids hammering the REST API on frequently-visited domains.

## Development

```bash
make build-local    # build for local arch
make test           # run unit tests
make lint           # golangci-lint (install separately)
make check-deps     # show available module updates
make update-deps    # apply updates
make vuln-check     # govulncheck CVE scan
```

## dlc.dat updates

Inside the container, crond runs `update-dlc.sh` every Sunday at 03:00.
After download it sends `SIGHUP` to `dns-proxy`, which reloads the geosite
database in-place without restarting or dropping DNS service.

Manual update from outside:
```bash
docker exec dns-geosite-proxy /app/update-dlc.sh
```

Or rebuild the database locally and copy to the router:
```bash
make download-dlc
scp data/dlc.dat admin@192.168.88.1:/usb1/data/
# then send SIGHUP inside container or restart it
```

## Project structure

```
dns-geosite-proxy/
├── src/
│   ├── main.go              signal handling (SIGHUP=reload, SIGTERM=exit)
│   ├── config/config.go     JSON config with Duration type for "336h" strings
│   ├── classifier/          domain -> tag + upstream (pre-compiled rules)
│   ├── geosite/loader.go    dlc.dat decoder via protowire (no codegen)
│   ├── dns/server.go        miekg/dns handler, UDP + TCP, TC retry
│   └── mikrotik/
│       ├── client.go        REST API client + FormatTimeout/ParseTimeout
│       └── addresslist.go   EnsureEntry: add / refresh / skip logic
├── docker/
│   ├── Dockerfile           multi-stage: builder(host arch) + runtime(arm64)
│   ├── entrypoint.sh        init: download dlc.dat -> crond -> exec dns-proxy
│   └── update-dlc.sh        curl + sanity check + SIGHUP
├── config.example.json
├── Makefile
└── LICENSE
```

## License

MIT - see [LICENSE](LICENSE).
