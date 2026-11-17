# Repository Agent Guidelines: Sunfire

Read [HANDOFF.md](HANDOFF.md) first, then [ROADMAP.md](ROADMAP.md), before starting work.
Before finishing any turn or session, record completed work, immediate next steps, and any blockers or gotchas in [HANDOFF.md](HANDOFF.md).

---

## Mandatory Commit Signing

Every commit created in this repository must have a valid cryptographic GPG signature:
- **Author / Signer:** Daniel Kill (`daniel@danielkill.online`)
- **Key Fingerprint:** `E453F9AEC380D33E4BA7FAC69B542AA270F867E5`
- **Signing Execution:** On `DANIELPREDPC`, execute signed git commits inside WSL2 (`daniel-kill` distro) to utilize the pre-authenticated `gpg-agent` session and avoid Windows GUI `pinentry.exe` desktop prompt timeouts:
  ```bash
  wsl.exe -d daniel-kill -e bash -c "cd /home/daniel/Repos/Sunfire && git commit -S -m 'type: description'"
  ```
- **Verification:** Always verify newly created commits:
  ```bash
  git log --show-signature -1
  ```
- **Remote Push Policy:** Never push commits to remote repositories (`git push`) without explicit confirmation from the user.

---

## Zero-Elevation & Security Policies

1. **Non-Elevated Execution:** Do not run commands requiring administrative or UAC elevation on Windows. On Linux/WSL, standard development and service commands run strictly under user mode (`systemctl --user`).
2. **Secrets & Privacy Isolation:**
   - Never commit API tokens, bot secrets, or configuration files containing credentials.
   - Local secrets reside strictly in `.private/config.json`, which is protected by `.gitignore`.
   - Never commit live message logs, career application details, or subscription records under `Data/`.

---

## Go Toolchain & Build Standards

1. **Toolchain Location:**
   - On `DANIELPREDPC`, execute Go commands inside WSL2 using the Linux Go binary at `/usr/local/go/bin/go` (Go 1.27.3).
   - Do NOT run Windows `go.exe` directly against the WSL filesystem path, as Plan9/9P network locking produces `RLock ...: Incorrect function` errors.
2. **Testing Protocol:**
   - Always run unit tests before committing:
     ```bash
     wsl.exe -d daniel-kill -e bash -c "cd /home/daniel/Repos/Sunfire && /usr/local/go/bin/go test ./..."
     ```
   - Tests must be deterministic and use `t.TempDir()` for state persistence; never read or write real production data in `Data/`.
3. **Compilation & Service Reloading:**
   - Compile the binary in WSL2:
     ```bash
     wsl.exe -d daniel-kill -e bash -c "cd /home/daniel/Repos/Sunfire && /usr/local/go/bin/go build -o Sunfire ."
     ```
   - Restart the running user service:
     ```bash
     wsl.exe -d daniel-kill -e bash -c "systemctl --user restart sunfire.service"
     ```
4. **Worker Lifecycle Safety:**
   - All background goroutines must be registered through `Bot.startWorkers()` and receive a cancellable `context.Context`.
   - Periodic jobs must use `runPeriodic()` to respect cancellation tokens and avoid hanging process shutdowns.
5. **Documentation Links:**
   - Always use relative markdown links between repository documents (e.g., `[README.md](README.md)`).

---

## Central Documentation & Knowledge Base

Consult central shared guides, runtimes, and agent documentation here:
- **Windows:** `C:\Users\danie\Repos\shared\docs\`
- **WSL:** `/home/daniel/Repos/shared/docs/`

Authoritative machine snapshots and installed packages:
- **Windows:** `C:\Users\danie\Repos\shared\data\snapshots\danielpredpc\`
- **WSL:** `/home/daniel/Repos/shared/data\snapshots\danielpredpc-wsl/`
