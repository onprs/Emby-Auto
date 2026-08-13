//go:build linux

package main

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRequest(t *testing.T) {
	t.Parallel()

	for _, request := range []controlRequest{
		{Command: "worker-status"},
		{Command: "worker-start"},
		{Command: "worker-stop"},
		{Command: "media-owner"},
		{Command: "host-network-counters"},
	} {
		if err := validateRequest(request); err != nil {
			t.Fatalf("validateRequest(%+v) error = %v", request, err)
		}
	}

	for _, request := range []controlRequest{{}, {Command: "shell"}, {Command: "apply"}} {
		if err := validateRequest(request); err == nil {
			t.Fatalf("validateRequest(%+v) error = nil", request)
		}
	}
}

func TestResolveMediaOwnerUIDUsesFixedEmbyAccount(t *testing.T) {
	requested := ""
	uid, err := resolveMediaOwnerUID(func(username string) (*user.User, error) {
		requested = username
		return &user.User{Username: "emby", Uid: "999"}, nil
	})
	if err != nil {
		t.Fatalf("resolveMediaOwnerUID() error = %v", err)
	}
	if requested != "emby" || uid != 999 {
		t.Fatalf("lookup = %q, UID = %d", requested, uid)
	}
}

func TestResolveMediaOwnerUIDRejectsMissingRootOrNonNumericAccount(t *testing.T) {
	tests := []struct {
		name string
		user *user.User
		err  error
	}{
		{name: "missing", err: os.ErrNotExist},
		{name: "root", user: &user.User{Username: "emby", Uid: "0"}},
		{name: "nonnumeric", user: &user.User{Username: "emby", Uid: "not-a-uid"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolveMediaOwnerUID(func(string) (*user.User, error) { return test.user, test.err }); err == nil {
				t.Fatal("resolveMediaOwnerUID() error = nil")
			}
		})
	}
}

func TestExecuteRequestRestrictsWorkerHelperArgumentsAndEnvironment(t *testing.T) {
	directory := t.TempDir()
	outputPath := filepath.Join(directory, "output")
	helperPath := filepath.Join(directory, "runtime-helper")
	helper := "#!/bin/sh\n" +
		"printf '%s\\n' \"$#\" \"$1\" \"${UNTRUSTED_ENVIRONMENT-unset}\" > \"" + outputPath + "\"\n" +
		"printf 'stopped\\n'\n"
	if err := os.WriteFile(helperPath, []byte(helper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UNTRUSTED_ENVIRONMENT", "must-not-pass")

	response := executeRequest(context.Background(), helperPath, controlRequest{Command: "worker-stop"})
	if response.Error != "" || response.Status != "stopped" {
		t.Fatalf("executeRequest() = %+v, want stopped", response)
	}
	output, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "1\nstop\nunset\n"; string(output) != want {
		t.Fatalf("runtime helper invocation = %q, want %q", output, want)
	}
}

func TestExecuteRequestRejectsInvalidWorkerStatus(t *testing.T) {
	directory := t.TempDir()
	helperPath := filepath.Join(directory, "runtime-helper")
	if err := os.WriteFile(helperPath, []byte("#!/bin/sh\nprintf 'unknown\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	response := executeRequest(context.Background(), helperPath, controlRequest{Command: "worker-status"})
	if !strings.Contains(response.Error, "invalid Worker status") {
		t.Fatalf("executeRequest() = %+v", response)
	}
}

// sampleNetDev 模拟宿主的 /proc/net/dev：物理网卡（eth0、ens18）应计入，
// loopback（lo）与逻辑接口（docker0、veth*、br0、br-*、tun0、bond0、wg0、
// VLAN 子接口 eth0.100）应排除，避免同一批流量被重复计数。
const sampleNetDev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:    1000      10    0    0    0     0          0         0     2000      10    0    0    0     0       0          0
  eth0:  100000   10000    0    0    0     0          0         0  200000   10000    0    0    0     0       0          0
 docker0:    500      50    0    0    0     0          0         0     600      50    0    0    0     0       0          0
vethAbc:    400      40    0    0    0     0          0         0     500      40    0    0    0     0       0          0
    br0:  300000   30000    0    0    0     0          0         0  400000   30000    0    0    0     0       0          0
 br-123:    300      30    0    0    0     0          0         0     400      30    0    0    0     0       0          0
  tun0:     200      20    0    0    0     0          0         0     300      20    0    0    0     0       0          0
 bond0: 5000000  500000    0    0    0     0          0         0 6000000  500000    0    0    0     0       0          0
   wg0:  700000   70000    0    0    0     0          0         0  800000   70000    0    0    0     0       0          0
eth0.100: 90000   9000    0    0    0     0          0         0  100000   9000    0    0    0     0       0          0
 ens18: 2000000  200000    0    0    0     0          0         0 4000000  200000    0    0    0     0       0          0
`

// fixtureSysfs 在临时目录创建模拟 sysfs：只有 eth0 与 ens18 是真实设备背板的
// 物理网卡（有 device 条目），其余接口均为逻辑接口。
func fixtureSysfs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"eth0", "ens18"} {
		if err := os.MkdirAll(filepath.Join(root, "class", "net", name, "device"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestReadHostNetworkCountersSumsPhysicalInterfacesOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "net-dev")
	if err := os.WriteFile(path, []byte(sampleNetDev), 0o644); err != nil {
		t.Fatal(err)
	}
	received, sent, err := readHostNetworkCounters(path, fixtureSysfs(t))
	if err != nil {
		t.Fatalf("readHostNetworkCounters() error = %v", err)
	}
	// eth0(100000/200000) + ens18(2000000/4000000)，其余接口全部排除。
	if received != 2_100_000 || sent != 4_200_000 {
		t.Fatalf("counters = %d/%d, want 2100000/4200000", received, sent)
	}
}

func TestReadHostNetworkCountersRejectsMissingFile(t *testing.T) {
	if _, _, err := readHostNetworkCounters(filepath.Join(t.TempDir(), "missing"), fixtureSysfs(t)); err == nil {
		t.Fatal("readHostNetworkCounters() error = nil")
	}
}

func TestReadHostNetworkCountersAllInterfacesExcluded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "net-dev")
	content := "Inter-|   Receive                                                |  Transmit\n" +
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
		"    lo:    100      10    0    0    0     0          0         0     200      10    0    0    0     0       0          0\n" +
		" docker0:   100      10    0    0    0     0          0         0     200      10    0    0    0     0       0          0\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	received, sent, err := readHostNetworkCounters(path, fixtureSysfs(t))
	if err != nil {
		t.Fatalf("readHostNetworkCounters() error = %v", err)
	}
	if received != 0 || sent != 0 {
		t.Fatalf("counters = %d/%d, want 0/0", received, sent)
	}
}

func TestReadHostNetworkCountersExcludesLogicalInterfacesSharingPhysicalTraffic(t *testing.T) {
	// bridge(bond0 成员场景由 eth0 计入)、bond0、VLAN 子接口 eth0.100 与 wg0
	// 都与底层物理网卡累计同一批流量，必须排除；只统计有 sysfs device 的
	// eth0/ens18，避免 Dashboard 显示约两倍流量。
	sysfsRoot := fixtureSysfs(t)
	path := filepath.Join(t.TempDir(), "net-dev")
	if err := os.WriteFile(path, []byte(sampleNetDev), 0o644); err != nil {
		t.Fatal(err)
	}
	received, sent, err := readHostNetworkCounters(path, sysfsRoot)
	if err != nil {
		t.Fatalf("readHostNetworkCounters() error = %v", err)
	}
	if received != 2_100_000 || sent != 4_200_000 {
		t.Fatalf("counters = %d/%d, want 2100000/4200000", received, sent)
	}
}

func TestReadHostNetworkCountersPreservesInterfaceNameCase(t *testing.T) {
	// Linux 接口名大小写敏感，WAN0 的真实 sysfs 路径是 /sys/class/net/WAN0/device；
	// 查询前不得小写化，否则会漏掉该物理网卡（只有它时返回 0 0 且仍标记可用）。
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "class", "net", "WAN0", "device"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "net-dev")
	content := "Inter-|   Receive                                                |  Transmit\n" +
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
		"    lo:    100      10    0    0    0     0          0         0     200      10    0    0    0     0       0          0\n" +
		"  WAN0:  150000   15000    0    0    0     0          0         0  250000   15000    0    0    0     0       0          0\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	received, sent, err := readHostNetworkCounters(path, root)
	if err != nil {
		t.Fatalf("readHostNetworkCounters() error = %v", err)
	}
	if received != 150_000 || sent != 250_000 {
		t.Fatalf("counters = %d/%d, want 150000/250000", received, sent)
	}
}

func TestExecuteRequestReadsHostNetworkCounters(t *testing.T) {
	response := executeRequest(context.Background(), t.TempDir()+"/unused-helper", controlRequest{Command: "host-network-counters"})
	if response.Error != "" {
		t.Fatalf("executeRequest() = %+v", response)
	}
	if response.NetworkReceiveBytes == nil || response.NetworkSendBytes == nil {
		t.Fatalf("executeRequest() counters = %+v", response)
	}
}
