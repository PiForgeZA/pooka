# Exec cancellation tests

## Unit tests

Run `go test -race ./...`.

The local transport test cancels a shell with a child process.
The supervisor test models the LXC proxy and a separate process session.
It also checks a parent that inherits ignored SIGHUP from `nohup`.
These tests do not need Proxmox.

## Integration tests

Use Linux for the client SIGKILL tests.
Use a test daemon built from the same checkout as the client.
Use a separate daemon port if another agent uses the installed daemon.

1. Build the client with `go build -o dist/pooka ./cmd/pooka`.
2. Set `POOKA_CONFIG` to a config for the test daemon.
3. Set `POOKA_INTEGRATION_CLI` to the absolute path of `dist/pooka`.
4. Set `POOKA_INTEGRATION_HOST` to the SSH host. The default is `root@proxmox`.
5. Set `POOKA_INTEGRATION_SPRITE` to the official CLI path to include its checks.
6. Run `go test -tags integration ./internal/backend/lxc -run TestExecDisconnectIntegration -v -count=1`.

The test uses `pooka create` to create a `fix3-*` environment.
It verifies the `pooka` tag before adding the `pooka-test` tag.
It destroys the environment on exit.
Do not interrupt the test process with SIGKILL. This prevents its cleanup function.

The matrix covers SSH and daemon exec, with and without a terminal, as pooka and
root. The command ignores HUP and TERM. The test kills the client with SIGKILL.
It checks the command PID through an independent SSH connection within one second.
The official CLI cases cover `--stdin`, `--http-post`, and `--tty`.
Other checks cover stdin EOF, exit codes, Ctrl-C, and interactive shell job control.

## Platform limits

Linux closes the dedicated SSH process when its client dies, including SIGKILL.
Other client platforms use context cancellation for SSH cleanup. Abrupt client
SIGKILL on those platforms is not covered.
The supervisor owns one process session. A command that creates another session
can leave that session outside this cleanup mechanism.
