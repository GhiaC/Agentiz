# Auth System Examples

این فایل مثال‌های استفاده از سیستم Auth جدید را نشان می‌دهد.

## مزایای ساختار جدید

### 1. **RBAC (Role-Based Access Control)**
به جای تعریف دسترسی برای هر کاربر، از نقش‌ها استفاده می‌کنیم:

```yaml
auth:
  roles:
    admin:
      perms: "rwx"  # Full access
    viewer:
      perms: "r"    # Read-only
    editor:
      perms: "rw"   # Read + Write
```

### 2. **Inheritance (ارث‌بری)**
نودهای فرزند می‌توانند دسترسی‌های parent را به ارث ببرند:

```yaml
# root/node.yaml
auth:
  default:
    perms: "r"  # Everyone can read by default
  roles:
    admin:
      perms: "rwx"

# root/child/node.yaml
auth:
  inherit: true  # Inherits from parent
  # No need to redefine roles!
```

### 3. **Groups (گروه‌بندی)**
کاربران را در گروه‌ها قرار دهید:

```yaml
auth:
  groups:
    developers:
      perms: "rwx"
    qa:
      perms: "r"
```

### 4. **Permission Strings (مثل Unix)**
استفاده از string به جای boolean flags:

```yaml
perms: "rwx"  # read, write, execute
perms: "r"    # read-only
perms: "rw"   # read + write
```

یا استفاده از boolean flags برای وضوح بیشتر:

```yaml
read: true
write: false
execute: true
```

## مثال‌های کامل

### مثال 1: ساده (فقط default)

```yaml
id: "public_node"
title: "Public Node"
auth:
  default:
    perms: "r"  # Everyone can read
```

### مثال 2: با Roles

```yaml
id: "admin_node"
title: "Admin Node"
auth:
  default:
    perms: ""  # Deny by default
  roles:
    admin:
      perms: "rwx"
    viewer:
      perms: "r"
```

### مثال 3: با Inheritance

```yaml
# root/node.yaml
id: "root"
auth:
  default:
    perms: "r"
  roles:
    admin:
      perms: "rwx"

# root/child/node.yaml
id: "child"
auth:
  inherit: true  # Inherits admin role from parent
  # Child nodes automatically get parent's permissions
```

### مثال 4: User Override

```yaml
id: "special_node"
auth:
  roles:
    admin:
      perms: "rwx"
  users:
    "user123":
      perms: "rw"  # Override: user123 can't execute even if admin
```

### مثال 5: Groups

```yaml
id: "dev_node"
auth:
  groups:
    developers:
      perms: "rwx"
    qa:
      perms: "r"
```

### مثال 6: Complex (همه چیز)

```yaml
id: "complex_node"
auth:
  inherit: true
  default:
    perms: "r"  # Default: read-only
  roles:
    admin:
      perms: "rwx"
    editor:
      perms: "rw"
    viewer:
      perms: "r"
  groups:
    developers:
      perms: "rwx"
  users:
    "special_user":
      perms: "rw"  # Override for specific user
```

## مقایسه با ساختار قبلی

### قبل (مشکل‌دار):
```yaml
auth:
  users:
    - user_id: "user1"
      can_edit: true
      can_read: true
      can_access_next: true
      can_see: true
      visible_in_docs: true
      visible_in_graph: true
    - user_id: "user2"
      can_edit: false
      can_read: true
      # ... باید برای هر کاربر تکرار شود
```

**مشکلات:**
- باید برای هر کاربر در هر نود تعریف شود
- Duplication زیاد
- مدیریت سخت
- نمی‌تواند از parent ارث‌بری کند

### بعد (بهتر):
```yaml
auth:
  roles:
    admin:
      perms: "rwx"
  default:
    perms: "r"
```

**مزایا:**
- یک بار تعریف می‌شود، همه جا استفاده می‌شود
- Inheritance از parent
- Groups و Roles
- مقیاس‌پذیرتر
- استاندارد دنیا (مثل Kubernetes, AWS IAM)

## Permission Flags

| Flag | Meaning | Boolean Equivalent |
|------|---------|-------------------|
| `r` | Read | `read: true` |
| `w` | Write/Edit | `write: true` |
| `x` | Execute/Access Next | `execute: true` |
| `s` | See | `see: true` |
| `d` | Visible in Docs | `visible_docs: true` |
| `g` | Visible in Graph | `visible_graph: true` |

## Priority Order

دسترسی‌ها به ترتیب زیر بررسی می‌شوند (اولین match برنده است):

1. **User-specific override** (بالاترین اولویت)
2. **Group permissions**
3. **Role permissions**
4. **Inherited from parent** (اگر `inherit: true`)
5. **Default permissions**
6. **Deny all** (اگر هیچکدام match نکرد)

