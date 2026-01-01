# استاندارد ثبت توابع Go برای Tools

## خلاصه

این استاندارد روش ثبت و استفاده از توابع Go برای tools را تعریف می‌کند. Tools از فایل‌های JSON در ساختار درختی خوانده می‌شوند، اما توابع Go باید از ابتدای پروژه ثبت شوند.

## معماری

```
┌─────────────────┐
│  tools.json     │  ← Tools از JSON خوانده می‌شوند (درختی)
│  (درخت)         │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  ToolRegistry   │  ← مدیریت tools
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│ FunctionRegistry│  ← ثبت توابع Go (از ابتدا)
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│     Engine      │  ← اجرای tools با توابع ثبت شده
└─────────────────┘
```

## استفاده

### 1. ایجاد و ثبت توابع

```go
import "agentize/model"

// ایجاد registry
functionRegistry := model.NewFunctionRegistry()

// ثبت توابع (در startup)
functionRegistry.MustRegister("tool_name", func(args map[string]interface{}) (string, error) {
    // پیاده‌سازی
    return "result", nil
})
```

### 2. اتصال به Engine

```go
eng := engine.NewEngineWithFunctions(
    repo,
    sessionStore,
    model.MergeStrategyOverride,
    functionRegistry,
)
```

### 3. اعتبارسنجی

```go
// بررسی اینکه همه tools دارای تابع هستند
if err := functionRegistry.ValidateAllTools(toolRegistry); err != nil {
    log.Fatalf("Missing functions: %v", err)
}
```

## قوانین

1. ✅ **همه توابع باید در startup ثبت شوند**
2. ✅ **نام تابع باید دقیقاً با نام tool در JSON مطابقت داشته باشد**
3. ✅ **اعتبارسنجی قبل از استفاده توصیه می‌شود**
4. ✅ **توابع باید از نوع `ToolFunction` باشند**

## مثال کامل

```go
func main() {
    // 1. ایجاد registry
    functionRegistry := model.NewFunctionRegistry()
    
    // 2. ثبت توابع
    functionRegistry.MustRegister("search_docs", searchDocsFunction)
    functionRegistry.MustRegister("query_db", queryDatabaseFunction)
    
    // 3. اعتبارسنجی
    ag, _ := agentize.New("./knowledge")
    toolRegistry := collectAllTools(ag)
    if err := functionRegistry.ValidateAllTools(toolRegistry); err != nil {
        log.Fatal(err)
    }
    
    // 4. استفاده
    eng := engine.NewEngineWithFunctions(repo, store, strategy, functionRegistry)
    // ...
}
```

## فایل‌های مرتبط

- `model/function_registry.go` - پیاده‌سازی FunctionRegistry
- `model/function_registry_test.go` - تست‌ها
- `engine/engine.go` - یکپارچه‌سازی با Engine
- `example/function_registry_example.go` - مثال کامل

