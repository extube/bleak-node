#!/bin/sh
# bleak-node container entrypoint.
#
# The server runs as uid 1000 (bleak), but the /app/data bind mount
# arrives with whatever ownership the host directory has — a missing or
# root-owned host dir makes docker create it as root:root, which leaves
# SQLite unable to create the db file / WAL-SHM sidecars
# ("unable to open database file (14)"). So: start as root, hand the
# data dir to bleak, drop privileges with busybox su (built into the
# base image — no packages fetched at build time), exec the server.
set -e

chown -R bleak:bleak /app/data
exec su -s /bin/sh -c 'exec /usr/local/bin/bleak-node' bleak
