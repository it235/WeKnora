# Custom CA Certificates

Drop any **PEM-encoded** internal / self-signed CA certificates here to make
WeKnora's `app` container trust them when calling private HTTPS endpoints
(e.g. an on-prem LLM gateway like `https://litellm.xxx.com`).

## Requirements

- File extension MUST be `.crt`
- Content MUST be PEM-encoded (`-----BEGIN CERTIFICATE-----` ... `-----END CERTIFICATE-----`)
- One certificate per file is recommended, but a chain in a single file also works

## How it works

`docker-compose.yml` bind-mounts this directory to
`/usr/local/share/ca-certificates/weknora-extra/` inside the container, and
`scripts/docker-entrypoint.sh` runs `update-ca-certificates` on startup. That
**appends** your CA(s) to the system bundle at
`/etc/ssl/certs/ca-certificates.crt`, which is what Go's `crypto/x509` reads
by default on Debian.

This means private + public CAs both work (no `SSL_CERT_FILE` override
needed — that env var would *replace* the bundle and break public HTTPS).

## Usage

1. Copy your CA file in:
   ```
   cp my-internal-ca.crt docker/certs/
   ```
2. Restart the app container:
   ```
   docker compose up -d app
   ```
3. Verify in the container logs:
   ```
   [entrypoint] found custom CA cert(s) in /usr/local/share/ca-certificates/weknora-extra, refreshing system trust store...
   [entrypoint][ca-certificates] 1 added, 0 removed; done.
   ```

## Verify trust manually

```bash
docker compose exec app bash -c \
  "curl -v https://litellm.xxx.com/v1/models 2>&1 | grep -E 'SSL|verify|certificate'"
```

If you still see `certificate signed by unknown authority`, double-check that:

- The file is the **CA** that signed the server cert, not the server cert itself
- The file is PEM (run `openssl x509 -in your.crt -text -noout` — should print)
- The container was actually restarted after dropping the file in
