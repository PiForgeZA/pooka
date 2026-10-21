package lxc

// The shell stays root so credential changes in runuser do not clear its
// parent-death signal. Each exec has its own session. Kill every group in that
// session, including an interactive shell's foreground job.
// Save stdin before starting the background command. Reset the signals that a
// non-interactive shell ignores for background jobs so Ctrl-C still works.
const execSupervisor = `trap 'pkill -KILL -s "$$"' HUP TERM
trap '' INT QUIT
exec 3<&0
env --default-signal=INT,QUIT "$@" <&3 3<&- &
child=$!
exec 3<&-
wait "$child"
exit $?`

func superviseExec(args []string, terminal bool) []string {
	// attach's arguments end with -- followed by the command.
	i := 0
	for args[i] != "--" {
		i++
	}
	out := append([]string{"setpriv", "--pdeathsig", "KILL"}, args[:i+1]...)
	if !terminal {
		out = append(out, "setsid")
	}
	// A daemon started with nohup inherits ignored SIGHUP. A shell cannot trap
	// a signal it inherited as ignored, so restore it before starting the shell.
	out = append(out, "env", "--default-signal=HUP,TERM", "setpriv", "--pdeathsig", "HUP", "sh", "-c", execSupervisor, "pooka-exec")
	return append(out, args[i+1:]...)
}
