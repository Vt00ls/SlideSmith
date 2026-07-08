#!/bin/sh
set -eu

container="${SLIDESMITH_AGENT_COMPOSE_CONTAINER:-slidesmith-agent-compose}"
workdir="${SLIDESMITH_AGENT_COMPOSE_CONTAINER_WORKDIR:-/data/work}"

exec docker exec -w "$workdir" "$container" agent-compose "$@"
