package agentmanager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/fsrepo"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
)

// newTestEngine returns a minimal Engine with only Functions set.
// IsDBReady will be false unless Init() is called with a valid repo and store.
func newTestEngine(tools ...string) *engine.Engine {
	eng := &engine.Engine{
		Functions: model.NewFunctionRegistry(),
	}
	for _, name := range tools {
		eng.Functions.MustRegister(name, name, func(args map[string]interface{}) (string, error) { return "", nil })
	}
	return eng
}

func TestRegister_Success(t *testing.T) {
	am := New(nil)
	cfg := AgentConfig{
		Name:         "researcher",
		DisplayName:  "Research Agent",
		Description:  "Does research",
		CostTier:     CostTierHigh,
		AgentType:    model.AgentType("researcher"),
		Capabilities: []string{"research", "coding"},
	}
	eng := newTestEngine("search")

	err := am.Register(cfg, eng)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	a, ok := am.Get("researcher")
	if !ok {
		t.Fatal("Get(researcher) should return true")
	}
	if a.Config.Name != "researcher" {
		t.Errorf("Name: got %q, want %q", a.Config.Name, "researcher")
	}
	if a.Config.DisplayName != "Research Agent" {
		t.Errorf("DisplayName: got %q, want %q", a.Config.DisplayName, "Research Agent")
	}
	if a.Config.CostTier != CostTierHigh {
		t.Errorf("CostTier: got %q, want high", a.Config.CostTier)
	}
	if a.Config.AgentType != model.AgentType("researcher") {
		t.Errorf("AgentType: got %q, want researcher", a.Config.AgentType)
	}
	if len(a.Config.Capabilities) != 2 || a.Config.Capabilities[0] != "research" {
		t.Errorf("Capabilities: got %v", a.Config.Capabilities)
	}
	if a.Engine != eng {
		t.Error("Engine reference should match")
	}
}

func TestRegister_EmptyName(t *testing.T) {
	am := New(nil)
	cfg := AgentConfig{Name: "", DisplayName: "X", CostTier: CostTierLow}
	eng := newTestEngine()

	err := am.Register(cfg, eng)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if err != nil && !strings.Contains(err.Error(), "name is required") {
		t.Errorf("error message: got %q", err.Error())
	}
}

func TestRegister_NilEngine(t *testing.T) {
	am := New(nil)
	cfg := AgentConfig{Name: "x", DisplayName: "X", CostTier: CostTierLow}

	err := am.Register(cfg, nil)
	if err == nil {
		t.Fatal("expected error for nil engine")
	}
	if err != nil && !strings.Contains(err.Error(), "engine is required") {
		t.Errorf("error message: got %q", err.Error())
	}
}

func TestRegister_DuplicateName(t *testing.T) {
	am := New(nil)
	cfg := AgentConfig{Name: "dup", DisplayName: "D", CostTier: CostTierLow}
	eng := newTestEngine()

	if err := am.Register(cfg, eng); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := am.Register(cfg, eng)
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}
	if err != nil && !strings.Contains(err.Error(), "already registered") {
		t.Errorf("error message: got %q", err.Error())
	}
}

func TestRegister_AutoAgentType(t *testing.T) {
	am := New(nil)
	cfg := AgentConfig{Name: "coder", DisplayName: "Coder", CostTier: CostTierMedium}
	// AgentType left empty
	eng := newTestEngine()

	err := am.Register(cfg, eng)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	a, ok := am.Get("coder")
	if !ok {
		t.Fatal("Get(coder) should return true")
	}
	if a.Config.AgentType != model.AgentType("coder") {
		t.Errorf("AgentType should be auto-set to name: got %q", a.Config.AgentType)
	}
}

func TestRegister_DisplayNamePropagation(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	sh := model.NewSessionHandler(sqliteStore, model.DefaultSessionHandlerConfig())
	am := New(sh)
	cfg := AgentConfig{
		Name:        "researcher",
		DisplayName: "Research Agent",
		CostTier:    CostTierHigh,
	}
	eng := newTestEngine()

	err = am.Register(cfg, eng)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	got := sh.AgentTypeDisplayName(model.AgentType("researcher"))
	if got != "Research Agent" {
		t.Errorf("AgentTypeDisplayName(researcher): got %q, want %q", got, "Research Agent")
	}
}

func TestGet_NotFound(t *testing.T) {
	am := New(nil)
	_, ok := am.Get("nonexistent")
	if ok {
		t.Error("Get(nonexistent) should return false")
	}
}

func TestGetAll_Sorted(t *testing.T) {
	am := New(nil)
	for _, name := range []string{"charlie", "alpha", "bravo"} {
		cfg := AgentConfig{Name: name, CostTier: CostTierLow}
		if err := am.Register(cfg, newTestEngine()); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}
	all := am.GetAll()
	if len(all) != 3 {
		t.Fatalf("GetAll: got %d agents, want 3", len(all))
	}
	if all[0].Config.Name != "alpha" || all[1].Config.Name != "bravo" || all[2].Config.Name != "charlie" {
		t.Errorf("GetAll order: got %q, %q, %q", all[0].Config.Name, all[1].Config.Name, all[2].Config.Name)
	}
}

func TestGetByTier(t *testing.T) {
	am := New(nil)
	am.Register(AgentConfig{Name: "low1", CostTier: CostTierLow}, newTestEngine())
	am.Register(AgentConfig{Name: "med1", CostTier: CostTierMedium}, newTestEngine())
	am.Register(AgentConfig{Name: "high1", CostTier: CostTierHigh}, newTestEngine())

	low := am.GetByTier(CostTierLow)
	if len(low) != 1 || low[0].Config.Name != "low1" {
		t.Errorf("GetByTier(low): got %v", low)
	}
}

func TestGetCheapest(t *testing.T) {
	am := New(nil)
	am.Register(AgentConfig{Name: "medium", CostTier: CostTierMedium}, newTestEngine())
	am.Register(AgentConfig{Name: "low", CostTier: CostTierLow}, newTestEngine())

	c := am.GetCheapest()
	if c == nil || c.Config.Name != "low" {
		if c == nil {
			t.Error("GetCheapest: got nil")
		} else {
			t.Errorf("GetCheapest: got %q, want low", c.Config.Name)
		}
	}
}

func TestGetCheapest_Empty(t *testing.T) {
	am := New(nil)
	c := am.GetCheapest()
	if c != nil {
		t.Errorf("GetCheapest on empty: got %v", c)
	}
}

func TestNames_Sorted(t *testing.T) {
	am := New(nil)
	am.Register(AgentConfig{Name: "z-agent", CostTier: CostTierLow}, newTestEngine())
	am.Register(AgentConfig{Name: "a-agent", CostTier: CostTierLow}, newTestEngine())

	names := am.Names()
	if len(names) != 2 || names[0] != "a-agent" || names[1] != "z-agent" {
		t.Errorf("Names: got %v", names)
	}
}

func TestCount(t *testing.T) {
	am := New(nil)
	am.Register(AgentConfig{Name: "a", CostTier: CostTierLow}, newTestEngine())
	am.Register(AgentConfig{Name: "b", CostTier: CostTierLow}, newTestEngine())
	am.Register(AgentConfig{Name: "c", CostTier: CostTierLow}, newTestEngine())

	if am.Count() != 3 {
		t.Errorf("Count: got %d, want 3", am.Count())
	}
}

func TestSetCallback(t *testing.T) {
	am := New(nil)
	eng1 := newTestEngine()
	eng2 := newTestEngine()
	am.Register(AgentConfig{Name: "a", CostTier: CostTierLow}, eng1)
	am.Register(AgentConfig{Name: "b", CostTier: CostTierLow}, eng2)

	var mockCb engine.Callback
	am.SetCallback(mockCb)

	if eng1.Callback != mockCb {
		t.Error("engine a Callback not set")
	}
	if eng2.Callback != mockCb {
		t.Error("engine b Callback not set")
	}
}

func TestIsReady_AllReady(t *testing.T) {
	tmpDir := createMinimalRepo(t)
	defer os.RemoveAll(tmpDir)
	repo, err := fsrepo.NewNodeRepository(tmpDir)
	if err != nil {
		t.Fatalf("NewNodeRepository: %v", err)
	}
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	eng1 := &engine.Engine{Repo: repo, Sessions: sqliteStore, Functions: model.NewFunctionRegistry()}
	if err := eng1.Init(); err != nil {
		t.Fatalf("eng1 Init: %v", err)
	}
	eng2 := &engine.Engine{Repo: repo, Sessions: sqliteStore, Functions: model.NewFunctionRegistry()}
	if err := eng2.Init(); err != nil {
		t.Fatalf("eng2 Init: %v", err)
	}

	am := New(nil)
	am.Register(AgentConfig{Name: "a", CostTier: CostTierLow}, eng1)
	am.Register(AgentConfig{Name: "b", CostTier: CostTierLow}, eng2)

	if !am.IsReady() {
		t.Error("IsReady: want true when all engines ready")
	}
}

func TestIsReady_OneNotReady(t *testing.T) {
	tmpDir := createMinimalRepo(t)
	defer os.RemoveAll(tmpDir)
	repo, err := fsrepo.NewNodeRepository(tmpDir)
	if err != nil {
		t.Fatalf("NewNodeRepository: %v", err)
	}
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	engReady := &engine.Engine{Repo: repo, Sessions: sqliteStore, Functions: model.NewFunctionRegistry()}
	if err := engReady.Init(); err != nil {
		t.Fatalf("engReady Init: %v", err)
	}
	engNotReady := newTestEngine()

	am := New(nil)
	am.Register(AgentConfig{Name: "ready", CostTier: CostTierLow}, engReady)
	am.Register(AgentConfig{Name: "notready", CostTier: CostTierLow}, engNotReady)

	if am.IsReady() {
		t.Error("IsReady: want false when one engine not ready")
	}
}

func createMinimalRepo(t *testing.T) string {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "agentmanager-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	rootPath := filepath.Join(tmpDir, "root")
	if err := os.MkdirAll(rootPath, 0755); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("MkdirAll root: %v", err)
	}
	yamlContent := `id: "root"
title: "Root"
`
	if err := os.WriteFile(filepath.Join(rootPath, "node.yaml"), []byte(yamlContent), 0644); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("WriteFile node.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "node.md"), []byte("# Root"), 0644); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("WriteFile node.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "tools.json"), []byte(`{"tools": []}`), 0644); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("WriteFile tools.json: %v", err)
	}
	return tmpDir
}
