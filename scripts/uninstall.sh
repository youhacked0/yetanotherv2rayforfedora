#!/bin/bash
set -e

echo "=== Uninstalling CustomV2Ray ==="

if [ "$EUID" -ne 0 ]; then
  echo "Error: this script must be run as root (use sudo)."
  exit 1
fi

if command -v customv2ray &> /dev/null; then
  customv2ray uninstall
elif [ -f "/usr/local/bin/customv2ray" ]; then
  /usr/local/bin/customv2ray uninstall
else
  echo "Stopping service if running..."
  systemctl stop customv2ray 2>/dev/null || true
  systemctl disable customv2ray 2>/dev/null || true
  rm -f /etc/systemd/system/customv2ray.service
  systemctl daemon-reload 2>/dev/null || true
  rm -f /usr/share/applications/customv2ray.desktop
  rm -f /usr/local/bin/v2ray /usr/local/bin/customv2ray
  rm -f /etc/NetworkManager/VPN/customv2ray.name
  rm -f /usr/share/dbus-1/system-services/org.freedesktop.NetworkManager.customv2ray.service
  rm -f /etc/dbus-1/system.d/customv2ray.conf
  echo "CustomV2Ray files removed."
fi

echo "=== Uninstall complete ==="

