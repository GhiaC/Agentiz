# نصب و راه‌اندازی Templ

برای استفاده از کامپوننت‌های templ، باید مراحل زیر را انجام دهید:

## 1. نصب Templ CLI

```bash
# روش 1: استفاده از go install
go install github.com/a-h/templ/cmd/templ@latest

# روش 2: اگر مشکل شبکه دارید، از homebrew استفاده کنید (macOS)
brew install templ

# روش 3: دانلود مستقیم از GitHub Releases
# به https://github.com/a-h/templ/releases بروید و نسخه مناسب را دانلود کنید
```

## 2. اضافه کردن Templ به go.mod

templ قبلاً به `go.mod` اضافه شده است. اگر نیاز به به‌روزرسانی دارید:

```bash
go get github.com/a-h/templ@latest
go mod tidy
```

## 3. تولید کد Go از فایل‌های Templ

پس از نصب templ CLI، دستور زیر را اجرا کنید:

```bash
templ generate
```

این دستور فایل‌های `.templ` را به فایل‌های `.go` تبدیل می‌کند.

## 4. استفاده در کد

پس از اجرای `templ generate`، می‌توانید از کامپوننت‌ها در کد Go استفاده کنید:

```go
import "github.com/ghiac/agentize/documents/components"

// استفاده از کامپوننت
components.Page(treeData, nodesData).Render(ctx, w)
```

## نکات مهم

- هر بار که فایل‌های `.templ` را تغییر می‌دهید، باید `templ generate` را دوباره اجرا کنید
- می‌توانید از `templ generate --watch` برای تولید خودکار هنگام تغییر فایل‌ها استفاده کنید
- فایل‌های تولید شده (`.templ.go`) را نباید به صورت دستی ویرایش کنید

