package fsrepo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghiac/agentize/model"
)

func parseMeta(t *testing.T, yaml string) *model.NodeMeta {
	t.Helper()
	meta := &model.NodeMeta{}
	if err := parseYAML([]byte(yaml), meta); err != nil {
		t.Fatalf("parseYAML: %v", err)
	}
	return meta
}

func TestParseYAML_CanonicalMapForm(t *testing.T) {
	meta := parseMeta(t, `
id: "node-1"
title: "Node One"
description: "desc"
summary: "sum"
auth:
  inherit: false
  default:
    perms: "r"
  users:
    "user-1":
      perms: "rw"
  roles:
    admin:
      perms: "rwx"
  groups:
    devs:
      read: true
      write: true
`)
	if meta.ID != "node-1" || meta.Title != "Node One" || meta.Summary != "sum" {
		t.Errorf("scalar fields: %+v", meta)
	}
	if meta.Auth.Inherit {
		t.Error("inherit: false not honored")
	}
	if meta.Auth.Default == nil || meta.Auth.Default.Perms != "r" {
		t.Errorf("default perms: %+v", meta.Auth.Default)
	}
	if p := meta.Auth.Users["user-1"]; p == nil || p.Perms != "rw" {
		t.Errorf("user perms: %+v", p)
	}
	if p := meta.Auth.Roles["admin"]; p == nil || p.Perms != "rwx" {
		t.Errorf("role perms: %+v", p)
	}
	if p := meta.Auth.Groups["devs"]; p == nil || !p.HasPermission('r') || !p.HasPermission('w') {
		t.Errorf("group perms: %+v", p)
	}
}

func TestParseYAML_LegacyListFormWithAliases(t *testing.T) {
	meta := parseMeta(t, `
id: "root"
auth:
  users:
    - user_id: "test"
      can_edit: true
      can_read: true
      can_access_next: true
      can_see: true
      visible_in_docs: true
      visible_in_graph: true
routing:
  mode: "sequential"
`)
	p := meta.Auth.Users["test"]
	if p == nil {
		t.Fatal("legacy list user not parsed")
	}
	for _, flag := range []rune{'r', 'w', 'x', 's', 'd', 'g'} {
		if !p.HasPermission(flag) {
			t.Errorf("permission %c not set", flag)
		}
	}
	if !meta.Auth.Inherit {
		t.Error("inherit must default to true")
	}
}

func TestParseYAML_LegacyBareUserForm(t *testing.T) {
	meta := parseMeta(t, `
auth:
  default:
    perms: "r"
  "user-42":
    perms: "rw"
`)
	if p := meta.Auth.Users["user-42"]; p == nil || p.Perms != "rw" {
		t.Errorf("bare-user form not parsed: %+v", meta.Auth.Users)
	}
}

func TestParseYAML_MalformedFailsLoudly(t *testing.T) {
	cases := map[string]string{
		"broken syntax":          "auth:\n  users:\n - : : :",
		"bad bool":               "auth:\n  inherit: maybe",
		"unknown permission key": "auth:\n  users:\n    \"u\":\n      can_fly: true",
		"non-mapping auth":       "auth: just-a-string",
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			meta := &model.NodeMeta{}
			if err := parseYAML([]byte(yaml), meta); err == nil {
				t.Errorf("malformed YAML accepted silently:\n%s", yaml)
			}
		})
	}
}

// A node directory with malformed node.yaml must fail LoadNode loudly instead
// of silently falling back to default permissions.
func TestLoadNode_MalformedYAMLFailsLoudly(t *testing.T) {
	tmpDir := t.TempDir()
	rootPath := filepath.Join(tmpDir, "root")
	os.MkdirAll(rootPath, 0o755)
	os.WriteFile(filepath.Join(rootPath, "node.yaml"), []byte("auth:\n  inherit: maybe\n"), 0o644)

	repo, err := NewNodeRepository(tmpDir)
	if err != nil {
		t.Fatalf("NewNodeRepository: %v", err)
	}
	if _, err := repo.LoadNode("root"); err == nil {
		t.Fatal("LoadNode accepted a node with malformed node.yaml")
	} else if !strings.Contains(err.Error(), "node.yaml") {
		t.Errorf("error should mention node.yaml: %v", err)
	}
}

// A node directory with NO node.yaml still loads with defaults.
func TestLoadNode_MissingYAMLUsesDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	rootPath := filepath.Join(tmpDir, "root")
	os.MkdirAll(rootPath, 0o755)
	os.WriteFile(filepath.Join(rootPath, "node.md"), []byte("# content"), 0o644)

	repo, err := NewNodeRepository(tmpDir)
	if err != nil {
		t.Fatalf("NewNodeRepository: %v", err)
	}
	node, err := repo.LoadNode("root")
	if err != nil {
		t.Fatalf("LoadNode: %v", err)
	}
	if node.ID != "root" || !node.Auth.Inherit {
		t.Errorf("defaults not applied: %+v", node)
	}
}
