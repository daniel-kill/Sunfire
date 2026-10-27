#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="/home/daniel/Repos/Sunfire"
LOG_FILE="$REPO_DIR/push.sh.log"

# Prompt for passwords at the start
GPG_KEY="E453F9AEC380D33E4BA7FAC69B542AA270F867E5"
echo -n "Sudo password (daniel): "
read -s SUDO_PASS
echo ""
echo -n "GPG passphrase ($GPG_KEY): "
read -s GPG_PASS
echo ""

# Unlock GPG agent so signed commits work non-interactively
echo "$GPG_PASS" | gpg-connect-agent "UPDATESTARTUPTTY" /bye 2>/dev/null || true
echo "" | gpg --batch --pinentry-mode loopback --passphrase "$GPG_PASS" --sign /dev/null 2>/dev/null || true

# Initialize log
{
  echo "========================================"
  echo "Sunfire push started: $(date)"
  echo "========================================"
  echo ""
} > "$LOG_FILE"

cd "$REPO_DIR"

log_step() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" | tee -a "$LOG_FILE"
}

log_step "Running tests..."
{
  go test ./... 2>&1
  rc=$?
  if [ $rc -ne 0 ]; then
    log_step "Tests failed (exit $rc), aborting."
    exit $rc
  fi
} | tee -a "$LOG_FILE"
log_step "Tests passed."
echo "" >> "$LOG_FILE"

log_step "Building Sunfire..."
{
  go build -o Sunfire . 2>&1
  rc=$?
  if [ $rc -ne 0 ]; then
    log_step "Build failed (exit $rc), aborting."
    exit $rc
  fi
} | tee -a "$LOG_FILE"
log_step "Build succeeded. Binary: $(ls -lh "$REPO_DIR/Sunfire" | awk '{print $5}')"
echo "" >> "$LOG_FILE"

log_step "Restarting service..."
{
  echo "$SUDO_PASS" | sudo -S systemctl restart Sunfire 2>&1
  rc=$?
  if [ $rc -ne 0 ]; then
    log_step "Restart failed (exit $rc)."
    exit $rc
  fi
} | tee -a "$LOG_FILE"
log_step "Service restarted."
echo "" >> "$LOG_FILE"

log_step "Updating documentation..."
{
  repo2md 2>&1
  rc=$?
  if [ $rc -ne 0 ]; then
    log_step "repo2md failed (exit $rc), continuing..."
  fi
} | tee -a "$LOG_FILE"
echo "" >> "$LOG_FILE"

log_step "Pushing to remote..."
{
  push-to-remote 2>&1
  rc=$?
  if [ $rc -ne 0 ]; then
    log_step "Push failed (exit $rc)."
    exit $rc
  fi
} | tee -a "$LOG_FILE"
log_step "Push completed."
echo "" >> "$LOG_FILE"

log_step "Done: $(date)"
