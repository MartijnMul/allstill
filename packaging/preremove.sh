#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Stops and disables the service when the package is removed, not when it is
# upgraded. The database in /var/lib/allstill stays.
set -e
case "$1" in
    upgrade | failed-upgrade | deconfigure) exit 0 ;; # dpkg
    '' | *[!0-9]*) ;;                                  # dpkg remove, apk
    0) ;;                                              # rpm: none left
    *) exit 0 ;;                                       # rpm: an upgrade
esac
if command -v systemctl > /dev/null && [ -d /run/systemd/system ]; then
    systemctl disable --now allstill.service || true
fi
