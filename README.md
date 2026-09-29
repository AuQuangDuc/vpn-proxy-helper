# VPN Proxy Helper

VPN Proxy Helper runs a SOCKS5 proxy whose outbound connections are bound to a
specific local network interface. This is useful when one Linux host has
multiple network interfaces connected to different networks and you want to
choose the egress path per proxy.

## Command-line usage

```text
Usage of vpn-proxy-helper:
  -i string
        Out going Local Network Interface Address or Interface Name
  -l string
        Bind address. Eg: unix:/tmp/.unix-socket/socks5.sock (default "127.0.0.1:1080")
  -debug
        Enable debug logging
  -update-interval duration
        Interface update check interval (default 2s)
```

Example:

```bash
./vpn-proxy-helper -i eth0 -l 127.0.0.1:1080
curl -x socks5://127.0.0.1:1080 https://1.1.1.1/cdn-cgi/trace
```

## Docker Compose: one proxy per network interface

The provided `docker-compose.yml` uses `network_mode: host`. This is important:
the proxy process inside the container needs to see the host network interfaces
directly so it can select the requested interface by name.

First, find the interface names on the host:

```bash
ip -br link
ip -br addr
```

Then edit the `command` of each service in `docker-compose.yml`:

```yaml
services:
  proxy-lan:
    command: ["-i", "eth0", "-l", "0.0.0.0:1080"]

  proxy-wifi:
    command: ["-i", "wlan0", "-l", "0.0.0.0:1081"]
```

For example:

```text
socks5://HOST_IP:1080 -> eth0  -> LAN network
socks5://HOST_IP:1081 -> wlan0 -> Wi-Fi network
```

Start all proxies:

```bash
docker compose up -d --build
```

Check logs:

```bash
docker compose logs -f
```

Stop them:

```bash
docker compose down
```

To add another network card, duplicate a service block, choose another
interface name, and assign a unique listening port. Because host networking is
used, do not add a Compose `ports:` mapping; the proxy binds directly to the
host port.

## Access from other LAN machines

The Compose example binds each SOCKS5 listener to `0.0.0.0`, so other machines
that can reach the host can connect to it. For example, if this host has the IP
`192.168.1.10`:

```bash
curl -x socks5://192.168.1.10:1080 https://1.1.1.1/cdn-cgi/trace
curl -x socks5://192.168.1.10:1081 https://1.1.1.1/cdn-cgi/trace
```

The SOCKS5 server currently has no authentication. Only expose these ports to
trusted networks, and restrict access with the host firewall when necessary.

## Architecture notes

The container image is built as a static Go binary and runs from `scratch` as a
non-root user. Building directly on an `aarch64` Linux host produces an ARM64
binary suitable for that host.
