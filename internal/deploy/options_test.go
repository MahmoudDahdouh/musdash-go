package deploy

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseRunOptionsAllows(t *testing.T) {
	cases := map[string][]string{
		"":                                nil,
		"   ":                             nil,
		"--init":                          {"--init"},
		"--shm-size 256m":                 {"--shm-size", "256m"},
		"--shm-size=1g --init":            {"--shm-size", "1g", "--init"},
		"--ulimit nofile=65536:65536":     {"--ulimit", "nofile=65536:65536"},
		"--add-host db.internal:10.0.0.5": {"--add-host", "db.internal:10.0.0.5"},
		"--add-host host.docker.internal:host-gateway": {"--add-host", "host.docker.internal:host-gateway"},
		"--hostname worker-1":                          {"--hostname", "worker-1"},
		"--user 1000:1000":                             {"--user", "1000:1000"},
		"--user root":                                  {"--user", "root"},
		"--workdir /app":                               {"--workdir", "/app"},
		"--stop-timeout 60 --stop-signal SIGINT":       {"--stop-timeout", "60", "--stop-signal", "SIGINT"},
		"--tmpfs /tmp:size=64m,noexec":                 {"--tmpfs", "/tmp:size=64m,noexec"},
		"--read-only --pids-limit 200":                 {"--read-only", "--pids-limit", "200"},
		"--sysctl net.core.somaxconn=1024":             {"--sysctl", "net.core.somaxconn=1024"},
		"--cap-add NET_BIND_SERVICE":                   {"--cap-add", "NET_BIND_SERVICE"},
		"--cap-add CAP_IPC_LOCK":                       {"--cap-add", "CAP_IPC_LOCK"},
		"--cap-drop ALL":                               {"--cap-drop", "ALL"},
		"--security-opt no-new-privileges":             {"--security-opt", "no-new-privileges"},
		"--hostname 'quoted-name'":                     {"--hostname", "quoted-name"},
		"--oom-score-adj":                              nil, // placeholder replaced below
	}
	delete(cases, "--oom-score-adj")
	for in, want := range cases {
		got, err := ParseRunOptions(in)
		if err != nil {
			t.Errorf("%q refused: %v", in, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestParseRunOptionsRefuses(t *testing.T) {
	bad := []string{
		"--privileged",
		"-v /:/host", "--volume /:/host", "--volume=/:/host", "--mount type=bind,source=/,target=/host",
		"--network host", "--net=host", "--pid host", "--ipc host", "--uts host", "--userns host",
		"--device /dev/sda", "--device-cgroup-rule 'c 1:3 mr'",
		"--cap-add SYS_ADMIN", "--cap-add ALL", "--cap-add NET_ADMIN", "--cap-add=SYS_PTRACE",
		"--security-opt seccomp=unconfined", "--security-opt apparmor=unconfined", "--security-opt label=disable",
		"--sysctl kernel.shmmax=1", "--sysctl fs.file-max=1",
		"--entrypoint /bin/sh", "--env-file /etc/shadow", "-e A=b", "--label musdash.managed=false",
		"--restart no", "--rm", "--name other", "--publish 0.0.0.0:80:80", "-p 80:80", "--expose 80",
		"--cgroup-parent /", "--runtime runc", "--volumes-from other", "--link other", "--gpus all",
		"--init--privileged", "--init=true", "--init --privileged",
		"--shm-size", "--shm-size --privileged", "--shm-size -1", "--shm-size 256m;rm",
		"--hostname --privileged", "--hostname a b", "--workdir relative", "--workdir /a,b",
		"--tmpfs /tmp:size=1m --privileged", "--user $(id)", "--add-host evil", "--dns example.com",
		"nginx", "--hostname 'unterminated",
		"--ulimit nofile=1 -v /:/host",
		// These would loosen the limits musdash sets on the container.
		"--memory-swap 64g", "--memory-swap=-1", "--cpu-shares 262144", "--cap-add SYS_NICE", "--cap-add CAP_SYS_NICE",
		"--memory 64g", "--cpus 64", "--oom-kill-disable", "--oom-score-adj -1000",
	}
	for _, in := range bad {
		if got, err := ParseRunOptions(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

func TestParsedOptionsNeverSmuggleAFlag(t *testing.T) {
	// Whatever is accepted, every argument is either an allowed flag or the
	// value that directly follows a flag that takes one.
	inputs := []string{"--init --read-only --shm-size 64m", "--ulimit nofile=1:2 --hostname x --cap-drop ALL"}
	for _, in := range inputs {
		args, err := ParseRunOptions(in)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < len(args); i++ {
			rule, ok := allowedOptions[args[i]]
			if !ok {
				t.Fatalf("%q produced a stray argument %q", in, args[i])
			}
			if rule != nil {
				i++
				if strings.HasPrefix(args[i], "--") {
					t.Fatalf("%q: value %q looks like a flag", in, args[i])
				}
			}
		}
	}
}
