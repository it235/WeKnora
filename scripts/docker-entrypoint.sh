#!/bin/bash
set -e

# ─── Fix ownership of bind-mounted directories ───
# When users bind-mount host directories (e.g. ./skills/preloaded),
# the mount inherits the host UID/GID which may differ from the
# container's appuser. This entrypoint runs as root, fixes ownership,
# then drops privileges to appuser via gosu — the same pattern used
# by official postgres/redis images.

# Directories that may be bind-mounted and need appuser access
MOUNT_DIRS=(
    /app/skills/preloaded
    /data/files
)

for dir in "${MOUNT_DIRS[@]}"; do
    if [ -d "$dir" ]; then
        chown -R appuser:appuser "$dir" 2>/dev/null || true
    fi
done

# ─── Merge built-in skills into preloaded ───
# Built-in skills are backed up at /app/skills/_builtin during image build.
# After a bind-mount replaces /app/skills/preloaded, copy back any
# missing built-in skills (without overwriting user-provided ones).
BUILTIN_DIR="/app/skills/_builtin"
PRELOADED_DIR="/app/skills/preloaded"

if [ -d "$BUILTIN_DIR" ]; then
    mkdir -p "$PRELOADED_DIR"
    for skill_dir in "$BUILTIN_DIR"/*/; do
        [ -d "$skill_dir" ] || continue
        skill_name="$(basename "$skill_dir")"
        if [ ! -d "$PRELOADED_DIR/$skill_name" ]; then
            cp -r "$skill_dir" "$PRELOADED_DIR/$skill_name"
        fi
    done
    chown -R appuser:appuser "$PRELOADED_DIR"
fi

# ─── Merge custom CA certificates into the system trust store ───
# Users mount their internal/self-signed CA certs into
# /usr/local/share/ca-certificates/weknora-extra/ via docker-compose.yml.
# Running update-ca-certificates appends them to /etc/ssl/certs/ca-certificates.crt,
# which is the default bundle Go's crypto/x509 reads on Debian. This way the
# private CA is *added* to (not *replacing*) the system bundle, so calls to
# both internal HTTPS endpoints (e.g. https://litellm.xxx.com) and public
# ones (api.openai.com, etc.) keep working.
EXTRA_CA_DIR="/usr/local/share/ca-certificates/weknora-extra"
if [ -d "$EXTRA_CA_DIR" ] && [ -n "$(ls -A "$EXTRA_CA_DIR" 2>/dev/null | grep -i '\.crt$' || true)" ]; then
    echo "[entrypoint] found custom CA cert(s) in $EXTRA_CA_DIR, refreshing system trust store..."
    update-ca-certificates 2>&1 | sed 's/^/[entrypoint][ca-certificates] /' || \
        echo "[entrypoint] WARN: update-ca-certificates failed; private CA may not be trusted"
fi

# ─── Drop privileges and exec the main process ───
exec gosu appuser "$@"
