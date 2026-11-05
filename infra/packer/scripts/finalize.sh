#!/usr/bin/env bash
# Last step of the Packer build: remove SSH (Session Manager is the only way in), strip build
# residue so every instance boots unique, and initialise the AIDE database over the final state.
# Ubuntu's ssh.service uses KillMode=process, so purging it keeps the current session alive.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

log() { printf 'finalize: %s\n' "$*"; }

log "removendo o OpenSSH, as chaves de host e o sudo sem senha do usuário de build"
systemctl disable --now ssh.socket ssh.service 2>/dev/null || true
apt-get purge -y -q openssh-server openssh-sftp-server >/dev/null
rm -f /etc/ssh/ssh_host_*
rm -rf /root/.ssh /home/ubuntu/.ssh
rm -f /etc/sudoers.d/90-cloud-init-users
passwd -l ubuntu >/dev/null
usermod --expiredate 1 ubuntu >/dev/null 2>&1 || true

log "limpando apt, cloud-init, journal e identidade da máquina"
apt-get autoremove -y -q --purge >/dev/null
apt-get clean
rm -rf /var/lib/apt/lists/*
cloud-init clean --logs --seed
journalctl --rotate >/dev/null 2>&1 || true
journalctl --vacuum-time=1s >/dev/null 2>&1 || true
find /var/log -type f \( -name '*.gz' -o -name '*.[0-9]' -o -name '*.old' \) -delete
for f in /var/log/wtmp /var/log/btmp /var/log/lastlog /var/log/auth.log /var/log/syslog /var/log/kern.log /var/log/dpkg.log /var/log/apt/history.log /var/log/apt/term.log; do
  [[ -f "$f" ]] && : > "$f"
done
rm -f /var/lib/dhcp/* /var/lib/systemd/random-seed
truncate -s 0 /etc/machine-id
rm -f /var/lib/dbus/machine-id
ln -s /etc/machine-id /var/lib/dbus/machine-id
rm -f /root/.bash_history /home/ubuntu/.bash_history
history -c 2>/dev/null || true

log "gravando as linhas de base do rkhunter e do AIDE sobre o estado final"
if command -v rkhunter >/dev/null 2>&1; then
  rkhunter --propupd --quiet >/dev/null 2>&1 || true
fi
if command -v aideinit >/dev/null 2>&1; then
  # Ubuntu's wrapper: regenerates the config from /etc/aide/aide.conf.d and builds the database.
  aideinit -y -f >/dev/null 2>&1 || true
  if [[ -f /var/lib/aide/aide.db.new ]]; then
    mv -f /var/lib/aide/aide.db.new /var/lib/aide/aide.db
  fi
fi

sync
log "concluído"
