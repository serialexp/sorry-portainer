package stacks

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/serialexp/sorry-portainer/internal/secrets"
)

const secretCompose = `# comment kept
services:
  app:
    image: busybox
    environment:
      DEBUG: on
      MODE: 0755
    secrets:
      - db_password
      - source: api_key
        target: /run/secrets/key
        uid: "1000"
        gid: 1000
        mode: 0400
  plain:
    image: busybox
secrets:
  db_password: {}
  api_key:
    external: true
`

func TestParseComposePlan(t *testing.T) {
	doc, err := parseCompose([]byte(secretCompose))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]secrets.Mount{"app": {
		{Source: "db_password", Target: "db_password", Mode: 0o444},
		{Source: "api_key", Target: "key", UID: 1000, GID: 1000, Mode: 0o400},
	}}
	if !reflect.DeepEqual(doc.plan.Services, want) {
		t.Fatalf("plan %+v", doc.plan.Services)
	}
	if !reflect.DeepEqual(doc.plan.Names, []string{"api_key", "db_password"}) {
		t.Fatalf("names %v", doc.plan.Names)
	}
}

func TestRuntimeComposeRewrite(t *testing.T) {
	doc, err := parseCompose([]byte(secretCompose))
	if err != nil {
		t.Fatal(err)
	}
	out, err := doc.runtimeCompose([]byte(secretCompose), "host-1", "web")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{"# comment kept", "DEBUG: on", "MODE: 0755", "io.sorry-portainer.agent: host-1", "io.sorry-portainer.stack: web", "/run/secrets:" + secrets.TmpfsOptions} {
		if !strings.Contains(text, want) {
			t.Errorf("runtime compose lacks %q:\n%s", want, text)
		}
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["secrets"]; ok {
		t.Error("top-level secrets survived")
	}
	services := parsed["services"].(map[string]any)
	if _, ok := services["app"].(map[string]any)["secrets"]; ok {
		t.Error("service secrets survived")
	}
	if _, ok := services["plain"].(map[string]any)["annotations"]; ok {
		t.Error("service without secrets got annotations")
	}
	deployed, err := deployedMounts(out)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(deployed, doc.plan.Services) {
		t.Fatalf("deployed %+v", deployed)
	}
}

func TestRuntimeComposeExtendsExistingAnnotationsAndTmpfs(t *testing.T) {
	for _, source := range []string{
		"services:\n  app:\n    image: x\n    annotations:\n      other: v\n    tmpfs: /tmp\n    secrets: [s]\nsecrets:\n  s:\n",
		"services:\n  app:\n    image: x\n    annotations:\n      - other=v\n    tmpfs:\n      - /tmp\n    secrets: [s]\nsecrets:\n  s:\n",
	} {
		doc, err := parseCompose([]byte(source))
		if err != nil {
			t.Fatal(err)
		}
		out, err := doc.runtimeCompose([]byte(source), "h", "web")
		if err != nil {
			t.Fatal(err)
		}
		deployed, err := deployedMounts(out)
		if err != nil || len(deployed["app"]) != 1 {
			t.Fatalf("deployed %+v %v\n%s", deployed, err, out)
		}
		text := string(out)
		if !strings.Contains(text, "other") || !strings.Contains(text, "/tmp") || !strings.Contains(text, "/run/secrets:") {
			t.Fatalf("lost existing entries:\n%s", text)
		}
	}
}

func TestRuntimeComposeWithoutSecretsIsUnchanged(t *testing.T) {
	source := []byte("services:\n  app:\n    image: x   # odd spacing kept\n")
	doc, err := parseCompose(source)
	if err != nil {
		t.Fatal(err)
	}
	out, err := doc.runtimeCompose(source, "h", "web")
	if err != nil || string(out) != string(source) {
		t.Fatalf("%q %v", out, err)
	}
}

func TestParseComposeRejects(t *testing.T) {
	cases := map[string]string{
		"file source":            "services: {}\nsecrets:\n  s:\n    file: ./s.txt\n",
		"environment source":     "services: {}\nsecrets:\n  s:\n    environment: S\n",
		"content source":         "services: {}\nsecrets:\n  s:\n    content: x\n",
		"driver":                 "services: {}\nsecrets:\n  s:\n    driver: shell\n",
		"undeclared":             "services:\n  a:\n    secrets: [s]\n",
		"bad name":               "services: {}\nsecrets:\n  ../x:\n",
		"bad target":             "services:\n  a:\n    secrets:\n      - source: s\n        target: /etc/s\nsecrets:\n  s:\n",
		"nested target":          "services:\n  a:\n    secrets:\n      - source: s\n        target: /run/secrets/a/b\nsecrets:\n  s:\n",
		"dot target":             "services:\n  a:\n    secrets:\n      - source: s\n        target: /run/secrets/../x\nsecrets:\n  s:\n",
		"duplicate target":       "services:\n  a:\n    secrets:\n      - s\n      - source: t\n        target: s\nsecrets:\n  s:\n  t:\n",
		"bad mode":               "services:\n  a:\n    secrets:\n      - source: s\n        mode: 01000\nsecrets:\n  s:\n",
		"bad uid":                "services:\n  a:\n    secrets:\n      - source: s\n        uid: -1\nsecrets:\n  s:\n",
		"unknown option":         "services:\n  a:\n    secrets:\n      - source: s\n        foo: 1\nsecrets:\n  s:\n",
		"reserved annotation":    "services:\n  a:\n    annotations:\n      io.sorry-portainer.stack: other\n",
		"reserved in list":       "services:\n  a:\n    annotations:\n      - io.sorry-portainer.agent=h\n",
		"reserved in label":      "services:\n  a:\n    labels:\n      x: io.sorry-portainer.secrets\n",
		"tmpfs conflict":         "services:\n  a:\n    tmpfs: /run/secrets\n    secrets: [s]\nsecrets:\n  s:\n",
		"volume conflict":        "services:\n  a:\n    volumes: [\"x:/run/secrets/s:ro\"]\n    secrets: [s]\nsecrets:\n  s:\n",
		"long volume conflict":   "services:\n  a:\n    volumes:\n      - type: bind\n        source: /x\n        target: /run/secrets\n    secrets: [s]\nsecrets:\n  s:\n",
		"merge brings secrets":   "x-base: &base\n  secrets: [s]\nservices:\n  a:\n    <<: *base\n    image: x\nsecrets:\n  s:\n",
		"merge brings tmpfs":     "x-base: &base\n  tmpfs: /tmp\nservices:\n  a:\n    <<: *base\n    secrets: [s]\nsecrets:\n  s:\n",
		"aliased service":        "x-base: &base\n  image: x\nservices:\n  a: *base\nsecrets:\n  s:\n",
		"extends file":           "services:\n  a:\n    extends:\n      file: /etc/other.yaml\n      service: b\n",
		"not a mapping":          "- a\n",
		"secrets not list":       "services:\n  a:\n    secrets: s\nsecrets:\n  s:\n",
		"secrets but no service": "secrets:\n  s:\n",
		"aliased annotations":    "x-a: &a\n  k: v\nservices:\n  a:\n    annotations: *a\n    secrets: [s]\nsecrets:\n  s:\n",
	}
	for name, source := range cases {
		if _, err := parseCompose([]byte(source)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseComposeAllowsHarmlessMerges(t *testing.T) {
	source := "x-env: &env\n  A: b\nx-base: &base\n  image: x\n  restart: always\nservices:\n  a:\n    <<: *base\n    environment: *env\n    secrets: [s]\nsecrets:\n  s:\n"
	doc, err := parseCompose([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.plan.Services["a"]) != 1 {
		t.Fatalf("plan %+v", doc.plan)
	}
}

func TestParseModeForms(t *testing.T) {
	for source, want := range map[string]uint32{"0440": 0o440, "0o440": 0o440, "288": 288, "\"0440\"": 0o440, "\"440\"": 0o440} {
		doc, err := parseCompose([]byte("services:\n  a:\n    secrets:\n      - source: s\n        mode: " + source + "\nsecrets:\n  s:\n"))
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		if got := doc.plan.Services["a"][0].Mode; got != want {
			t.Errorf("%s: mode %o, want %o", source, got, want)
		}
	}
}
