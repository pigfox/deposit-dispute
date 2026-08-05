#!/usr/bin/env bash
#
# with-env.sh — source .env and run a command, without ever echoing what it read.
#
# WHY THIS EXISTS. Every credential this repo uses arrives through the
# environment, and the one thing that must never happen is a value reaching a
# terminal, a log or a transcript. Sourcing inline in an ad-hoc shell invitation
# is how that happens: a stray `set -x`, an `env | grep`, or a command that
# echoes its own arguments, and a key is in the scrollback forever.
#
# So there is one wrapper. It sources, it runs, and it prints nothing about what
# it loaded — not the names, not a count, and certainly not a value.
#
# Usage:
#   ./scripts/with-env.sh 'forge script script/Deploy.s.sol --rpc-url "$RPC_URL" ...'
#   ./scripts/with-env.sh 'cast chain-id --rpc-url "$RPC_URL"'
#
# The command is ONE quoted argument, evaluated after the environment is loaded,
# so "$RPC_URL" inside it expands from .env rather than from the caller's shell.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$HERE/.env"

if [ "$#" -ne 1 ]; then
	echo "usage: with-env.sh 'COMMAND'" >&2
	exit 2
fi

if [ ! -f "$ENV_FILE" ]; then
	echo "with-env.sh: no .env at the repository root." >&2
	echo "Copy .env.example to .env and fill it in. Nothing in it is ever committed." >&2
	exit 1
fi

# `set -a` exports everything the file defines; the subshell keeps it out of the
# caller's environment. Redirecting the source itself guards against a .env that
# has somehow acquired an echo.
set -a
# shellcheck disable=SC1090
. "$ENV_FILE" >/dev/null
set +a

eval "$1"
