package provcommon

import "github.com/leandrodaf/barnraise/internal/ports"

// HostArgs builds the command line of a session that runs on the machine
// itself (not in a container): confined by Barnraise when the spec says so,
// then as the account's Linux user. The confinement goes inside sudo, so the
// layer Barnraise applies holds for the agent's user too.
func HostArgs(spec ports.SessionSpec, env *Env, name string, args []string) (string, []string, error) {
	name, args, err := spec.Confine.Apply(name, args)
	if err != nil {
		return "", nil, err
	}
	if spec.RunAsUser != "" {
		name, args = SudoArgs(spec.RunAsUser, env, name, args)
	}
	return name, args, nil
}
