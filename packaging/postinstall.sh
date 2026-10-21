#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Makes the user the service runs as. A new install does not start the
# service: set the base URL in /etc/allstill/allstill.env first, then enable
# it. An upgrade restarts a running service, so the new version serves and
# no admin command migrates the database under the old one.
set -e
if ! getent passwd allstill > /dev/null; then
    if command -v useradd > /dev/null; then
        useradd --system --user-group --home-dir /var/lib/allstill --no-create-home --shell /usr/sbin/nologin allstill
    else
        addgroup -S allstill
        adduser -S -D -H -G allstill -h /var/lib/allstill -s /sbin/nologin allstill
    fi
fi
install -d -o allstill -g allstill -m 0750 /var/lib/allstill

# An upgrade: dpkg passes "configure" and the old version, rpm a count of 2 or more.
upgrade=no
case "$1" in
    configure) [ -n "$2" ] && upgrade=yes ;;
    [0-9]*) [ "$1" -ge 2 ] 2> /dev/null && upgrade=yes ;;
esac
if command -v systemctl > /dev/null && [ -d /run/systemd/system ]; then
    systemctl daemon-reload
    if [ "$upgrade" = yes ]; then
        systemctl try-restart allstill.service
    fi
fi
