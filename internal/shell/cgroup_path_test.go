//go:build linux

package shell

import "testing"

func TestCgroupV2PathStripsTheDelegatedSupervisorSubgroup(t *testing.T) {
	cases := map[string]string{
		"0::/system.slice/gradops.service/supervisor\n": "system.slice/gradops.service",
		"0::/system.slice/gradops.service\n":            "system.slice/gradops.service",
		"0::/supervisor\n":                              "",
		"1:name=systemd:/legacy\n0::/user.slice/x\n":    "user.slice/x",
	}
	for input, want := range cases {
		got, err := cgroupV2Path(input)
		if err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		if got != want {
			t.Fatalf("%q: got %q, want %q", input, got, want)
		}
	}
}

func TestCgroupV2PathRejectsMissingV2Entry(t *testing.T) {
	if _, err := cgroupV2Path("1:name=systemd:/legacy\n"); err == nil {
		t.Fatal("expected an error for a cgroup v1-only proc file")
	}
}
