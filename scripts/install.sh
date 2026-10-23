#!/bin/bash
set -e

echo "=== Installing CustomV2Ray for Fedora 44 ==="

if [ "$EUID" -ne 0 ]; then
  echo "Error: this script must be run as root (use sudo)."
  exit 1
fi

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR"

echo "Stopping customv2ray service if running..."
systemctl stop customv2ray 2>/dev/null || true

echo "Building customv2ray binary..."
go build -ldflags="-s -w" -o customv2ray ./cmd/customv2ray

echo "Installing binary to /usr/local/bin..."
cp -f customv2ray /usr/local/bin/customv2ray.tmp
mv -f /usr/local/bin/customv2ray.tmp /usr/local/bin/customv2ray
chmod 755 /usr/local/bin/customv2ray
ln -sf /usr/local/bin/customv2ray /usr/local/bin/v2ray

echo "Running installer..."
/usr/local/bin/customv2ray install

echo "Checking if xray is installed..."
if ! command -v xray &> /dev/null; then
  echo "NOTE: 'xray' binary not found in PATH."
  echo "You can install Xray via:"
  echo "  sudo dnf install xray-core   # if available in repo"
  echo "  or bash -c \"\$(curl -L https://github.com/XTLS/Xray-install/raw/main/install-release.sh)\" @ install"
fi

echo "=== Done! You can now use 'customv2ray' or 'v2ray' ==="

