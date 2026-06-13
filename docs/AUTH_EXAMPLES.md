# Auth System Examples

> 🇮🇷 نسخه فارسی: [AUTH_EXAMPLES_FA.md](AUTH_EXAMPLES_FA.md)

This file shows how to use the node authorization (`auth:`) system that governs
access to knowledge-tree nodes.

## Why this structure

### 1. RBAC (Role-Based Access Control)
Instead of defining permissions per user, use roles:

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

### 2. Inheritance
Child nodes can inherit the parent's permissions:

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
  inherit: true  # Inherits from the parent
  # No need to redefine roles!
```

### 3. Groups
Place users into groups:

```yaml
auth:
  groups:
    developers:
      perms: "rwx"
    qa:
      perms: "r"
```

### 4. Permission strings (Unix-style)
Use a string instead of boolean flags:

```yaml
perms: "rwx"  # read, write, execute
perms: "r"    # read-only
perms: "rw"   # read + write
```

…or use boolean flags for clarity:

```yaml
read: true
write: false
execute: true
```

## Complete examples

### Example 1 — simple (default only)

```yaml
id: "public_node"
title: "Public Node"
auth:
  default:
    perms: "r"  # Everyone can read
```

### Example 2 — with roles

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

### Example 3 — with inheritance

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
  inherit: true  # Inherits the admin role from the parent
  # Child nodes automatically get the parent's permissions
```

### Example 4 — user override

```yaml
id: "special_node"
auth:
  roles:
    admin:
      perms: "rwx"
  users:
    "user123":
      perms: "rw"  # Override: user123 cannot execute, even as admin
```

### Example 5 — groups

```yaml
id: "dev_node"
auth:
  groups:
    developers:
      perms: "rwx"
    qa:
      perms: "r"
```

### Example 6 — complex (everything together)

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
      perms: "rw"  # Override for a specific user
```

## Comparison with the old structure

### Before (problematic):
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
      # ... had to be repeated for every user
```

**Problems:**
- Had to be defined for every user on every node
- Heavy duplication
- Hard to manage
- Could not inherit from the parent

### After (better):
```yaml
auth:
  roles:
    admin:
      perms: "rwx"
  default:
    perms: "r"
```

**Benefits:**
- Defined once, used everywhere
- Inheritance from the parent
- Groups and roles
- More scalable
- An industry-standard model (like Kubernetes, AWS IAM)

## Permission flags

| Flag | Meaning | Boolean equivalent |
|------|---------|--------------------|
| `r` | Read | `read: true` |
| `w` | Write/Edit | `write: true` |
| `x` | Execute / Access next | `execute: true` |
| `s` | See | `see: true` |
| `d` | Visible in docs | `visible_docs: true` |
| `g` | Visible in graph | `visible_graph: true` |

## Priority order

Permissions are evaluated in the following order (first match wins):

1. **User-specific override** (highest priority)
2. **Group permissions**
3. **Role permissions**
4. **Inherited from parent** (if `inherit: true`)
5. **Default permissions**
6. **Deny all** (if nothing matched)

---

> **Note:** this `auth:` block controls access to *knowledge-tree nodes*. It is
> separate from the **admin authentication** that protects the `/agentize`
> dashboard and metrics endpoint — see [SECURITY.md](SECURITY.md) and
> `SetAdminCredentials` / `AGENTIZE_ADMIN_USERNAME` / `AGENTIZE_ADMIN_PASSWORD`.
