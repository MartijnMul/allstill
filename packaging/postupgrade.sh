#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Alpine's upgrade: restart a running service, as postinstall.sh does for the
# other packages.
set -e
if command -v systemctl > /dev/null && [ -d /run/systemd/system ]; then
    systemctl daemon-reload
    systemctl try-restart allstill.service
fi
