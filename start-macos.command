#!/bin/sh
# Finder-friendly macOS launcher. Double-click this file after the package has
# been approved by Gatekeeper (or after removing quarantine from a trusted copy).
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec "$DIR/start.sh"
