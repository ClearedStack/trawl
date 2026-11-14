package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var digestPinnedImage = regexp.MustCompile(`^[a-z0-9./:-]+@sha256:[0-9a-f]{64}$`)

// TestDeployManifestsAndBuildConfig checks the Kubernetes manifests in
// deploy/ and the ko build file against the ClearedStack cluster rules.
func TestDeployManifestsAndBuildConfig(t *testing.T) {
	root := filepath.Join("..", "..")

	read := func(rel string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := string(raw)
		if !utf8.ValidString(text) {
			t.Fatalf("%s is not valid UTF-8", rel)
		}
		if strings.Contains(text, "\t") {
			t.Errorf("%s contains tabs; YAML must use spaces", rel)
		}
		if strings.Contains(text, ":latest") {
			t.Errorf("%s uses a floating :latest tag instead of a digest", rel)
		}
		return text
	}

	ko := read(".ko.yaml")
	var baseImage string
	for _, line := range strings.Split(ko, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "defaultBaseImage:"); ok {
			baseImage = strings.TrimSpace(rest)
		}
	}
	if !digestPinnedImage.MatchString(baseImage) {
		t.Errorf(".ko.yaml defaultBaseImage = %q, want image pinned by @sha256 digest", baseImage)
	}
	for _, flag := range []string{"CGO_ENABLED=0", "-trimpath", "-buildid="} {
		if !strings.Contains(ko, flag) {
			t.Errorf(".ko.yaml missing %q", flag)
		}
	}

	ns := read(filepath.Join("deploy", "namespace.yaml"))
	for _, want := range []string{"kind: Namespace", "name: cs-workers"} {
		if !strings.Contains(ns, want) {
			t.Errorf("deploy/namespace.yaml missing %q", want)
		}
	}

	trawlYAML := read(filepath.Join("deploy", "trawl.yaml"))
	var workloadImage string
	for _, line := range strings.Split(trawlYAML, "\n") {
		trimmed := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(trimmed, "namespace:"); ok {
			if got := strings.TrimSpace(rest); got != "cs-workers" {
				t.Errorf("deploy/trawl.yaml has namespace %q, want cs-workers", got)
			}
		}
		if rest, ok := strings.CutPrefix(trimmed, "image:"); ok {
			workloadImage = strings.TrimSpace(rest)
		}
	}
	if !digestPinnedImage.MatchString(workloadImage) || !strings.HasPrefix(workloadImage, "lazarette.clearedstack.svc.cluster.local:5000/trawl@sha256:") {
		t.Errorf("deploy/trawl.yaml image = %q, want lazarette.../trawl@sha256:<digest>", workloadImage)
	}
	for _, required := range []string{
		"kind: ServiceAccount",
		"kind: ConfigMap",
		"kind: Deployment",
		"namespace: cs-workers",
		"serviceAccountName: trawl",
		"runAsNonRoot: true",
		"runAsUser: 65532",
		"readOnlyRootFilesystem: true",
		"allowPrivilegeEscalation: false",
		"privileged: false",
		"type: RuntimeDefault",
		"drop:\n                - ALL",
		"key: kubernetes.io/arch\n                    operator: In\n                    values:\n                      - arm64",
		"key: kubernetes.io/hostname\n                    operator: NotIn\n                    values:\n                      - cm3588-plus",
		"requests:\n              cpu: 10m\n              memory: 16Mi",
		"limits:\n              cpu: 100m\n              memory: 30Mi",
		"name: CONTRACT_VERSION\n              value: v1",
		"mountPath: /var/spool/cs",
	} {
		if !strings.Contains(trawlYAML, required) {
			t.Errorf("deploy/trawl.yaml missing required block:\n%s", required)
		}
	}

	netpol := read(filepath.Join("deploy", "networkpolicy.yaml"))
	for _, line := range strings.Split(netpol, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "namespace:"); ok {
			if got := strings.TrimSpace(rest); got != "cs-workers" {
				t.Errorf("deploy/networkpolicy.yaml has namespace %q, want cs-workers", got)
			}
		}
		if strings.HasPrefix(strings.TrimSpace(line), "ingress:") {
			t.Errorf("deploy/networkpolicy.yaml opens ingress (%q); Trawl accepts no inbound traffic", line)
		}
	}
	for _, required := range []string{
		"name: default-deny",
		"podSelector: {}",
		"- Ingress\n    - Egress",
		"kubernetes.io/metadata.name: kube-system",
		"kubernetes.io/metadata.name: clearedstack",
		"app.kubernetes.io/name: muster",
		"cidr: 0.0.0.0/0",
		"- 10.0.0.0/8",
		"- 127.0.0.0/8",
		"- 169.254.0.0/16",
		"- 172.16.0.0/12",
		"- 192.168.0.0/16",
	} {
		if !strings.Contains(netpol, required) {
			t.Errorf("deploy/networkpolicy.yaml missing required block:\n%s", required)
		}
	}
}

// TestRepositoryHygiene enforces the repository-wide ClearedStack rules:
// Go standard library only, no secrets or keys in Git, and no em dashes
// anywhere in code or documentation.
func TestRepositoryHygiene(t *testing.T) {
	root := filepath.Join("..", "..")

	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gomod), "module github.com/ClearedStack/trawl") {
		t.Errorf("unexpected module path in go.mod:\n%s", gomod)
	}
	if strings.Contains(string(gomod), "require") {
		t.Errorf("go.mod declares external dependencies; standard library only:\n%s", gomod)
	}
	if _, err := os.Stat(filepath.Join(root, "go.sum")); !os.IsNotExist(err) {
		t.Errorf("go.sum should not exist when only the standard library is used")
	}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !utf8.Valid(raw) {
			return nil
		}
		text := string(raw)
		rel, _ := filepath.Rel(root, path)
		if strings.ContainsRune(text, '\u2014') {
			t.Errorf("%s contains an em dash (U+2014)", rel)
		}
		if rel != filepath.Join("cmd", "trawl", "deploy_test.go") {
			for _, forbidden := range []string{"PRIVATE" + " KEY-----", "kind:" + " Secret"} {
				if strings.Contains(text, forbidden) {
					t.Errorf("%s contains forbidden secret marker %q", rel, forbidden)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
