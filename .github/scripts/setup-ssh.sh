#!/usr/bin/env bash
# Prepare SSH for a deploy job without writing the private key to disk.
#
# Loads SSH_KEY into an ssh-agent, pins host keys from SSH_KNOWN_HOSTS, and
# maps HOST_ALIAS to HOST_NAME so the real address never appears in
# commands or logs. Agent variables are exported to later steps through
# GITHUB_ENV.
set -euo pipefail

for var in SSH_KEY SSH_KNOWN_HOSTS HOST_ALIAS HOST_NAME HOST_USER; do
  if [[ -z "${!var:-}" ]]; then
    echo "Missing $var; check the deploy secrets." >&2
    exit 1
  fi
done

umask 077
mkdir -p ~/.ssh
printf '%s\n' "$SSH_KNOWN_HOSTS" > ~/.ssh/known_hosts
cat > ~/.ssh/config <<CONFIG
Host $HOST_ALIAS
  HostName $HOST_NAME
  User $HOST_USER

Host *
  BatchMode yes
  StrictHostKeyChecking yes
  UserKnownHostsFile ~/.ssh/known_hosts
  ServerAliveInterval 30
CONFIG

eval "$(ssh-agent -s)" >/dev/null
printf '%s\n' "$SSH_KEY" | ssh-add - 2>/dev/null
{
  echo "SSH_AUTH_SOCK=$SSH_AUTH_SOCK"
  echo "SSH_AGENT_PID=$SSH_AGENT_PID"
} >> "$GITHUB_ENV"
