<div align="center">

  ![Loggd Banner](https://raw.githubusercontent.com/loggdme/.github/refs/heads/main/.github/assets/header.webp)
  
  # Simple Go Proxy
  Simple HTTP/HTTPS proxy written in go and distributed via Docker.
</div>

<br>

## Configuration

The proxy is configured entirely via environment variables.

| Variable             | Default | Description                                                                        |
|----------------------|---------|------------------------------------------------------------------------------------|
| `PROXY_PORT`         | `8888`  | Port to listen on                                                                  |
| `PROXY_AUTH`         | —       | Basic auth as `username:password`. No auth if unset.                               |
| `PROXY_TLS_CERT_B64` | —       | Base64-encoded PEM certificate. Enables HTTPS listener when set together with key. |
| `PROXY_TLS_KEY_B64`  | —       | Base64-encoded PEM private key.                                                    |
| `PROXY_DIAL_TIMEOUT` | `30s`   | Upstream connection timeout (e.g. `10s`, `1m`).                                    |

## Generate TLS certificates

```bash
mise run gen-certs
# or with a custom hostname:
mise run gen-certs myproxy.example.com
```

This writes `proxy.crt` and `proxy.key` to the current directory and prints the base64 env vars:

```
=== Base64 env vars ===
PROXY_TLS_CERT_B64=LS0tLS1CRUdJTi...
PROXY_TLS_KEY_B64=LS0tLS1CRUdJTi...
```

## Running with Docker

### HTTP (no TLS)

```bash
docker run -d \
  -e PROXY_AUTH=user:secret \
  -p 8888:8888 \
  ghcr.io/loggdme/simple-go-proxy
```

Test it:

```bash
# plain HTTP target
curl --proxy http://user:secret@localhost:8888 http://httpbin.org/ip

# HTTPS target (tunneled via CONNECT)
curl --proxy http://user:secret@localhost:8888 https://httpbin.org/ip
```

### HTTPS (TLS on the proxy listener)

First generate certs, then pass them as env vars:

```bash
mise run gen-certs

docker run -d \
  -e PROXY_AUTH=user:secret \
  -e PROXY_TLS_CERT_B64="$(base64 < proxy.crt | tr -d '\n')" \
  -e PROXY_TLS_KEY_B64="$(base64 < proxy.key | tr -d '\n')" \
  -p 8888:8888 \
  ghcr.io/loggdme/simple-go-proxy
```

Test it (pass `--proxy-cacert` so curl trusts the self-signed cert):

```bash
# plain HTTP target
curl --proxy https://user:secret@localhost:8888 \
  --proxy-cacert proxy.crt \
  http://httpbin.org/ip

# HTTPS target
curl --proxy https://user:secret@localhost:8888 \
  --proxy-cacert proxy.crt \
  https://httpbin.org/ip
```

## License

This project and each package it provides is licensed under the MIT License - see the [LICENSE](LICENSE) file for more details.