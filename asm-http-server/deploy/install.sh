#!/bin/bash
# One-shot install on this machine. Run with: sudo ./deploy/install.sh
set -euo pipefail
cd "$(dirname "$0")/.."
pacman -S --needed --noconfirm caddy
make
mkdir -p /home/mike/files && chown mike:mike /home/mike/files
install -m 644 deploy/asm-files.service /etc/systemd/system/asm-files.service
if grep -q REPLACE_WITH_BCRYPT_HASH deploy/Caddyfile; then
  echo "Choose the password for https://files.deufel.dev (user: mike):"
  HASH=$(caddy hash-password)
  sed "s|REPLACE_WITH_BCRYPT_HASH|$HASH|" deploy/Caddyfile > /etc/caddy/Caddyfile
else
  install -m 644 deploy/Caddyfile /etc/caddy/Caddyfile
fi
systemctl daemon-reload
systemctl enable --now asm-files.service
systemctl enable --now caddy
systemctl restart caddy
echo
echo "asm-files: $(systemctl is-active asm-files)   caddy: $(systemctl is-active caddy)"
echo "Now make sure ports 80 and 443 reach this machine, then open https://files.deufel.dev"
