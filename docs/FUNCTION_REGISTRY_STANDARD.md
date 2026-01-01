# استاندارد ثبت توابع Go برای Tools

این سند استاندارد ثبت و استفاده از توابع Go برای tools را توضیح می‌دهد.

## مشکل

Tools از فایل‌های JSON در ساختار درختی خوانده می‌شوند، اما توابع Go باید از ابتدای پروژه ثبت شوند و همه متدها باید وجود داشته باشند.

## راه‌حل: FunctionRegistry

استفاده از `FunctionRegistry` برای ثبت و مدیریت توابع Go که با tools مرتبط هستند.

## استفاده

### 1. ایجاد FunctionRegistry

```go
import "agentize/model"

// ایجاد registry
functionRegistry := model.NewFunctionRegistry()
```

### 2. ثبت توابع

**مهم:** تمام توابع باید در زمان startup برنامه ثبت شوند.

```go
// ثبت تک‌تک
functionRegistry.MustRegister("tool_name", func(args map[string]interface{}) (string, error) {
    // پیاده‌سازی تابع
    return "result", nil
})

// یا ثبت دسته‌ای
registrations := map[string]model.ToolFunction{
    "tool1": func(args map[string]interface{}) (string, error) {
        return "result1", nil
    },
    "tool2": func(args map[string]interface{}) (string, error) {
        return "result2", nil
    },
}
functionRegistry.RegisterBatch(registrations)
```

### 3. استفاده در Engine

```go
import (
    "agentize/engine"
    "agentize/fsrepo"
    "agentize/model"
    "agentize/store"
)

// ایجاد repository و session store
repo, _ := fsrepo.NewNodeRepository(knowledgePath)
sessionStore := store.NewMemoryStore()

// ایجاد engine با function registry
eng := engine.NewEngineWithFunctions(
    repo,
    sessionStore,
    model.MergeStrategyOverride,
    functionRegistry,
)

// یا استفاده از SetFunctionRegistry
eng := engine.NewEngine(repo, sessionStore, model.MergeStrategyOverride)
eng.SetFunctionRegistry(functionRegistry)
```

### 4. اعتبارسنجی (Validation)

قبل از استفاده، توصیه می‌شود که تمام tools دارای تابع باشند:

```go
// بارگذاری تمام nodes
ag, _ := agentize.New(knowledgePath)
allNodes := ag.GetAllNodes()

// ایجاد tool registry و جمع‌آوری تمام tools
toolRegistry := model.NewToolRegistry(model.MergeStrategyOverride)
for _, node := range allNodes {
    toolRegistry.AddTools(node.Tools)
}

// اعتبارسنجی
if err := functionRegistry.ValidateAllTools(toolRegistry); err != nil {
    log.Fatalf("Some tools are missing functions: %v", err)
}
```

## ساختار تابع

تمام توابع باید از نوع `ToolFunction` باشند:

```go
type ToolFunction func(args map[string]interface{}) (string, error)
```

### مثال پیاده‌سازی

```go
func searchDocsFunction(args map[string]interface{}) (string, error) {
    // استخراج پارامترها
    query, ok := args["q"].(string)
    if !ok {
        return "", fmt.Errorf("missing or invalid 'q' parameter")
    }
    
    // انجام عملیات
    result := performSearch(query)
    
    // بازگرداندن نتیجه
    return result, nil
}

// ثبت
functionRegistry.MustRegister("search_docs", searchDocsFunction)
```

## Best Practices

### 1. ثبت در Startup

همه توابع باید در `main()` یا تابع initialization ثبت شوند:

```go
func init() {
    // یا در main()
    registerAllToolFunctions()
}

func registerAllToolFunctions() {
    functionRegistry.MustRegister("tool1", tool1Function)
    functionRegistry.MustRegister("tool2", tool2Function)
    // ...
}
```

### 2. اعتبارسنجی در Startup

همیشه قبل از شروع استفاده، اعتبارسنجی کنید:

```go
func main() {
    // ثبت توابع
    functionRegistry := model.NewFunctionRegistry()
    registerAllToolFunctions(functionRegistry)
    
    // اعتبارسنجی
    ag, _ := agentize.New(knowledgePath)
    toolRegistry := collectAllTools(ag)
    if err := functionRegistry.ValidateAllTools(toolRegistry); err != nil {
        log.Fatalf("Missing functions: %v", err)
    }
    
    // ادامه کار
}
```

### 3. مدیریت خطا

همیشه خطاها را به درستی handle کنید:

```go
func myToolFunction(args map[string]interface{}) (string, error) {
    // بررسی پارامترها
    param, ok := args["param"].(string)
    if !ok {
        return "", fmt.Errorf("missing required parameter 'param'")
    }
    
    // انجام عملیات با مدیریت خطا
    result, err := doSomething(param)
    if err != nil {
        return "", fmt.Errorf("operation failed: %w", err)
    }
    
    return result, nil
}
```

### 4. Thread Safety

`FunctionRegistry` thread-safe است و می‌تواند در goroutine‌ها استفاده شود.

## مثال کامل

```go
package main

import (
    "fmt"
    "log"
    
    "agentize"
    "agentize/engine"
    "agentize/fsrepo"
    "agentize/model"
    "agentize/store"
)

func main() {
    knowledgePath := "./knowledge"
    
    // 1. ایجاد function registry
    functionRegistry := model.NewFunctionRegistry()
    
    // 2. ثبت تمام توابع
    registerAllToolFunctions(functionRegistry)
    
    // 3. اعتبارسنجی
    ag, err := agentize.New(knowledgePath)
    if err != nil {
        log.Fatalf("Failed to load knowledge: %v", err)
    }
    
    toolRegistry := collectAllTools(ag)
    if err := functionRegistry.ValidateAllTools(toolRegistry); err != nil {
        log.Fatalf("Validation failed: %v", err)
    }
    
    // 4. ایجاد engine
    repo, _ := fsrepo.NewNodeRepository(knowledgePath)
    sessionStore := store.NewMemoryStore()
    eng := engine.NewEngineWithFunctions(
        repo,
        sessionStore,
        model.MergeStrategyOverride,
        functionRegistry,
    )
    
    // 5. استفاده
    session, _ := eng.StartSession("user123")
    fmt.Printf("Session started: %s\n", session.SessionID)
}

func registerAllToolFunctions(registry *model.FunctionRegistry) {
    registry.MustRegister("search_docs", searchDocsFunction)
    registry.MustRegister("query_db", queryDatabaseFunction)
    // ... سایر توابع
}

func collectAllTools(ag *agentize.Agentize) *model.ToolRegistry {
    toolRegistry := model.NewToolRegistry(model.MergeStrategyOverride)
    allNodes := ag.GetAllNodes()
    for _, node := range allNodes {
        toolRegistry.AddTools(node.Tools)
    }
    return toolRegistry
}

func searchDocsFunction(args map[string]interface{}) (string, error) {
    query := args["q"].(string)
    return fmt.Sprintf("Found results for: %s", query), nil
}

func queryDatabaseFunction(args map[string]interface{}) (string, error) {
    sql := args["sql"].(string)
    return fmt.Sprintf("Query result: %s", sql), nil
}
```

## خلاصه

1. ✅ **FunctionRegistry** برای ثبت توابع استفاده می‌شود
2. ✅ تمام توابع باید در **startup** ثبت شوند
3. ✅ **اعتبارسنجی** قبل از استفاده توصیه می‌شود
4. ✅ توابع باید از نوع `ToolFunction` باشند
5. ✅ Engine از FunctionRegistry برای اجرای tools استفاده می‌کند

