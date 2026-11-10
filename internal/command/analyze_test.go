package command_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/gdemonc/grad-ops/internal/command"
)

func TestAnalyzeAcceptsReadOnlyScripts(t *testing.T) {
	scripts := map[string]string{
		"uname":                      "uname -a",
		"disk":                       "df -h /",
		"variable argument":          `printf '%s\n' "$value"`,
		"pipeline":                   "cat /var/log/app.log | grep -i error",
		"find":                       "find /var/log -name '*.log' -type f",
		"systemctl status":           "systemctl status nginx",
		"journalctl":                 "journalctl -u nginx --since today",
		"processes":                  "ps aux | grep nginx",
		"shell state":                "export X=1; cd /var/log",
		"stderr duplicate":           "printf x >&2",
		"awk fields":                 `awk '{print $1}' /var/log/app.log`,
		"sort stdout":                "sort /var/log/app.log",
		"tar list":                   "tar -tf archive.tar",
		"service status":             "service nginx status",
		"systemctl variable service": `systemctl status "$svc"`,
		"absolute path":              "/bin/cat /var/log/app.log",
		"redirect to null":           "cat /var/log/app.log >/dev/null 2>&1",
		"command inspect":            "command -v bash",
		"dpkg query":                 "dpkg -l",
		"apt query":                  "apt list --installed",
		"iptables list":              "iptables -L",
		"nft list":                   "nft list ruleset",
		"ufw status":                 "ufw status",
		"crontab list":               "crontab -l -u root",
		"wget stdout":                "wget -qO- https://example.com",
		"curl null":                  "curl -s -o /dev/null https://example.com",
	}

	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			analysis, err := command.Analyze(script)
			if err != nil {
				t.Fatalf("analyze: %v", err)
			}
			if !analysis.ReadOnly() {
				t.Fatalf("expected read-only, got %q", analysis.Summary())
			}
		})
	}
}

func TestAnalyzeRejectsWrites(t *testing.T) {
	scripts := map[string]string{
		"rm":              "rm -rf /var/log/app",
		"touch":           "touch /tmp/x",
		"mkdir":           "mkdir nested",
		"mv":              "mv a b",
		"redirect":        "echo hi > /tmp/x",
		"append":          "echo hi >> /tmp/x",
		"tee":             "printf x | tee /tmp/y",
		"sed in place":    "sed -i 's/a/b/' /tmp/x",
		"find delete":     "find /tmp -delete",
		"find fprint":     "find /tmp -fprint out",
		"systemctl":       "systemctl restart nginx",
		"service":         "service nginx restart",
		"journal vacuum":  "journalctl --vacuum-time=1d",
		"mount":           "mount /dev/sda1 /mnt",
		"ip":              "ip link set eth0 down",
		"sysctl":          "sysctl -w net.ipv4.ip_forward=1",
		"date":            "date -s 2020-01-01",
		"hostname":        "hostname newname",
		"crontab":         "crontab -e",
		"curl output":     "curl -o /tmp/x https://example.com",
		"tar extract":     "tar -xf archive.tar",
		"sort output":     "sort -o out in",
		"kill":            "kill 123",
		"absolute path":   "/bin/rm -rf /var/log/app",
		"command execute": "command -p rm /tmp/x",
		"dpkg install":    "dpkg -i package.deb",
		"iptables flush":  "iptables -F",
		"crontab edit":    "crontab -e",
		"wget file":       "wget -O /tmp/x https://example.com",
		"curl file":       "curl -o /tmp/x https://example.com",
		"sed combined":    "sed -ni 's/a/b/p' /tmp/x",
		"sort attached":   "sort -oout in",
	}

	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			analysis, err := command.Analyze(script)
			if err != nil {
				t.Fatalf("analyze: %v", err)
			}
			if analysis.ReadOnly() {
				t.Fatalf("expected a write finding for %q", script)
			}
			if !errors.Is(analysis.Err(), command.ErrUnsafeScript) {
				t.Fatalf("expected ErrUnsafeScript, got %v", analysis.Err())
			}
		})
	}
}

func TestAnalyzeRejectsIndirectExecution(t *testing.T) {
	scripts := map[string]string{
		"bash -c":           "bash -c 'echo hi'",
		"eval":              `eval "$x"`,
		"source":            "source ./script",
		"xargs":             "xargs rm",
		"find exec":         "find /tmp -exec rm {} \\;",
		"awk system":        `awk 'BEGIN{system("id")}'`,
		"awk spaced system": `awk 'BEGIN{ system ( "id" ) }'`,
		"ssh":               "ssh host ls",
		"python":            "python3 -c 'print(1)'",
		"env":               "env FOO=bar ls",
		"command":           "command ls",
	}

	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			analysis, err := command.Analyze(script)
			if err != nil {
				t.Fatalf("analyze: %v", err)
			}
			if analysis.ReadOnly() {
				t.Fatalf("expected an indirect-execution finding for %q", script)
			}
			if len(analysis.Indirect) == 0 {
				t.Fatalf("expected Indirect findings, got %+v", analysis)
			}
		})
	}
}

func TestAnalyzeRejectsDynamicCommandNames(t *testing.T) {
	for name, script := range map[string]string{
		"variable":     "$cmd --version",
		"quoted":       `"$cmd" --version`,
		"substitution": "$(printf rm) -rf /tmp/x",
	} {
		t.Run(name, func(t *testing.T) {
			analysis, err := command.Analyze(script)
			if err != nil {
				t.Fatalf("analyze: %v", err)
			}
			if analysis.ReadOnly() {
				t.Fatalf("expected a dynamic finding for %q", script)
			}
			if len(analysis.Dynamic) == 0 {
				t.Fatalf("expected Dynamic findings, got %+v", analysis)
			}
		})
	}
}

func TestAnalyzeRejectsDynamicSystemctlSubcommand(t *testing.T) {
	analysis, err := command.Analyze(`systemctl "$action" nginx`)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if analysis.ReadOnly() {
		t.Fatalf("a dynamic systemctl subcommand must not look read-only: %+v", analysis)
	}
	if !strings.Contains(analysis.Summary(), "assembled at runtime") {
		t.Fatalf("expected a dynamic reason, got %q", analysis.Summary())
	}
}

func TestAnalyzeRejectsMalformedScripts(t *testing.T) {
	if _, err := command.Analyze("if then"); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestAnalyzeReportsNetworkWithoutCallingItAWrite(t *testing.T) {
	analysis, err := command.Analyze("curl https://example.com")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if len(analysis.Network) != 1 || analysis.Network[0] != "curl" {
		t.Fatalf("expected curl in Network, got %+v", analysis.Network)
	}
	if len(analysis.Writes) != 0 {
		t.Fatalf("a plain curl should not be reported as a file write: %+v", analysis.Writes)
	}
}
