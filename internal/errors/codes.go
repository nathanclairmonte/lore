package errors

// ExitCode is the process exit code class for a lore error. The same code
// means the same class of failure across every subcommand so scripts and
// agents can react uniformly. The exact cause goes in the error Message.
type ExitCode int

const (
	CodeSuccess       ExitCode = 0
	CodeGenericError  ExitCode = 1
	CodeSetupMissing  ExitCode = 2
	CodeNotFound      ExitCode = 3
	CodeConflict      ExitCode = 4
	CodeRefusedSafety ExitCode = 5
	CodeGitNetwork    ExitCode = 6
	CodeInconsistency ExitCode = 7
)
