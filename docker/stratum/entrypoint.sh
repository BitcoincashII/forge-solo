#!/bin/sh
set -e
# Render the stratum config from the template using the container env, then run.
envsubst < /etc/forge/config.template.yaml > /etc/forge/config.yaml
exec /usr/local/bin/stratum -config /etc/forge/config.yaml
