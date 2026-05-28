#!/bin/bash
#
# sync-to-darkoob.sh — همگام‌سازی یک‌طرفه Agentize → darkoob-platform
#
# کپی فایل‌های اضافه/تغییرکرده از Agentize به کپی داخل darkoob و جایگزینی
# آدرس import مطابق go.mod مقصد. برای فایل‌هایی که فقط در مقصد هستند می‌توان
# رفتار را با --new-in-dest مشخص کرد یا تعاملی پرسید.
#
# استفاده:
#   ./scripts/sync-to-darkoob.sh [OPTIONS] [SOURCE] [DEST]
#   SOURCE/DEST می‌توانند با متغیر محیطی AGENTIZE_SYNC_SRC و AGENTIZE_SYNC_DEST هم تنظیم شوند.
#
# گزینه‌ها:
#   --dry-run              فقط لیست عملیات، بدون کپی
#   -v, --verbose          چاپ هر فایل در حین پردازش
#   --new-in-dest copy     فایل‌های فقط در مقصد → کپی به مبدا
#   --new-in-dest ignore   نادیده گرفتن (پیش‌فرض اگر تعاملی جواب ندهید)
#   --new-in-dest delete   حذف از مقصد
#   اگر --new-in-dest ندهید، لیست فایل‌های «فقط در مقصد» چاپ و از شما پرسیده می‌شود.
#

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_SOURCE="$(cd "$SCRIPT_DIR/.." && pwd)"
DEFAULT_DEST="/Users/masoud/GolandProjects/darkoob-platform/services/infra_agent/pkg/agentize"

SOURCE="${AGENTIZE_SYNC_SRC:-$DEFAULT_SOURCE}"
DEST="${AGENTIZE_SYNC_DEST:-$DEFAULT_DEST}"
DRY_RUN=false
VERBOSE=false
NEW_IN_DEST=""   # copy | ignore | delete

# Import path replace (Agentize module → darkoob in-repo path)
REPLACE_FROM="github.com/ghiac/agentize"
REPLACE_TO="anbar.bale.ai/bale/darkoob-platform/services/infra_agent/pkg/agentize"

# Text extensions that get import path replacement
TEXT_EXTS="go templ md yaml yml mod"

# Parse arguments
ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)   DRY_RUN=true; shift ;;
    -v|--verbose) VERBOSE=true; shift ;;
    --new-in-dest)
      shift
      if [[ "$1" != "copy" && "$1" != "ignore" && "$1" != "delete" ]]; then
        echo -e "${RED}Error: --new-in-dest requires copy|ignore|delete${NC}" >&2
        exit 1
      fi
      NEW_IN_DEST="$1"
      shift
      ;;
    *) ARGS+=("$1"); shift ;;
  esac
done
[[ ${#ARGS[@]} -ge 1 ]] && SOURCE="${ARGS[0]}"
[[ ${#ARGS[@]} -ge 2 ]] && DEST="${ARGS[1]}"

SOURCE="$(cd "$SOURCE" && pwd)"
[[ ! -d "$DEST" ]] && mkdir -p "$DEST"
DEST="$(cd "$DEST" && pwd)"

# Exclude paths (relative): not synced
is_excluded() {
  local r="$1"
  case "$r" in
    .git|.git/*|.gitignore|.DS_Store|.idea|.idea/*) return 0 ;;
    go.mod|go.sum|Makefile|coverage.out) return 0 ;;
    README.md) return 0 ;;
    scripts/sync-to-darkoob.sh) return 0 ;;
    *.db) return 0 ;;
    *) return 1 ;;
  esac
}

# Return 0 if path has a text extension we replace in
is_text_for_replace() {
  local r="$1"
  for ext in $TEXT_EXTS; do
    [[ "$r" == *".$ext" ]] && return 0
  done
  return 1
}

# Output normalized content of source file (as it would look in dest after replace) for comparison
get_normalized_src() {
  local f="$1" r="$2"
  if is_text_for_replace "$r"; then
    sed "s|$REPLACE_FROM|$REPLACE_TO|g" "$f" 2>/dev/null || cat "$f"
  else
    cat "$f"
  fi
}

# Print diff line stats: "+N -M" or "new file, L lines"
diff_change_summary() {
  local dest_file="$1" norm_src_file="$2" src_file="$3" rel="$4"
  if [[ ! -f "$dest_file" ]]; then
    local lines
    lines=$(wc -l < "$src_file" 2>/dev/null || echo 0)
    echo " (new file, $lines lines)"
    return
  fi
  local diff_out added removed
  diff_out=$(diff -u "$dest_file" "$norm_src_file" 2>/dev/null || true)
  added=$(echo "$diff_out" | grep '^+' | grep -v '^+++' | wc -l | tr -d ' ')
  removed=$(echo "$diff_out" | grep '^-' | grep -v '^---' | wc -l | tr -d ' ')
  echo " (+${added} -${removed} lines)"
}

# Returns 0 if ALL changed lines in a unified diff are Go import-related.
# Covers: import keyword, quoted paths, aliased imports, "log" in line, bare ")".
is_import_only_diff() {
  local diff_text="$1"
  [[ -z "$diff_text" ]] && return 1
  while IFS= read -r line; do
    case "$line" in "---"*|"+++"*|"@@"*) continue ;; esac
    [[ "$line" != "+"* && "$line" != "-"* ]] && continue
    local content="${line:1}"
    local stripped="${content#"${content%%[![:space:]]*}"}"
    [[ -z "$stripped" ]] && continue
    [[ "$stripped" == "import"* ]] && continue
    [[ "$stripped" == ")" ]] && continue
    [[ "$stripped" == \"* ]] && continue
    [[ "$stripped" =~ ^[a-zA-Z_][a-zA-Z0-9_]*[[:space:]]+\" ]] && continue
    [[ "$stripped" == *"log"* ]] && continue
    return 1
  done <<< "$diff_text"
  return 0
}

# Apply import path replace in file (in-place).
# Usage: apply_replace FILE FROM_PATTERN TO_PATTERN
apply_replace() {
  local f="$1" from="${2:-$REPLACE_FROM}" to="${3:-$REPLACE_TO}"
  if ! is_text_for_replace "$f"; then return 0; fi
  if [[ ! -f "$f" ]]; then return 0; fi
  if "$DRY_RUN"; then return 0; fi
  if perl -e '' 2>/dev/null; then
    perl -i -pe "s|\Q$from\E|$to|g" "$f"
  else
    sed -i.bak "s|$from|$to|g" "$f" && rm -f "${f}.bak"
  fi
}

# List relative paths of regular files under dir, excluding excluded
list_files() {
  local dir="$1"
  (cd "$dir" && find . -type f) | sed 's|^\./||' | while read -r rel; do
    is_excluded "$rel" || echo "$rel"
  done
}

# Collect into arrays (bash 4+ or use files). We use temp files for portability.
TMP_SRC="$(mktemp)" TMP_DEST="$(mktemp)" TMP_ONLY_DEST="$(mktemp)" TMP_NORM="$(mktemp)"
trap 'rm -f "$TMP_SRC" "$TMP_DEST" "$TMP_ONLY_DEST" "$TMP_NORM"' EXIT

list_files "$SOURCE" | sort -u > "$TMP_SRC"
list_files "$DEST"   | sort -u > "$TMP_DEST"

# Files only in dest (relative paths)
comm -23 "$TMP_DEST" "$TMP_SRC" > "$TMP_ONLY_DEST" || true

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}  Agentize → darkoob sync${NC}"
echo -e "${BLUE}========================================${NC}"
echo "Source: $SOURCE"
echo "Dest:   $DEST"
if "$DRY_RUN"; then echo -e "${YELLOW}(dry-run: no changes)${NC}"; fi
echo ""

COPIED=0
REPLACED=0
SKIPPED=0

# 1) Copy source → dest for new or changed files (compare after normalizing import path); then replace in dest
while IFS= read -r rel; do
  src_file="$SOURCE/$rel"
  dest_file="$DEST/$rel"
  do_copy=false
  if [[ ! -f "$dest_file" ]]; then
    do_copy=true
  else
    get_normalized_src "$src_file" "$rel" > "$TMP_NORM"
    if ! cmp -s "$TMP_NORM" "$dest_file"; then
      if [[ "$rel" == *.go ]]; then
        _imp_diff=$(diff -u "$dest_file" "$TMP_NORM" 2>/dev/null || true)
        if is_import_only_diff "$_imp_diff"; then
          $VERBOSE && echo -e "${BLUE}[skip import-only]${NC} $rel"
          ((SKIPPED++)) || true
          continue
        fi
      fi
      do_copy=true
    fi
  fi
  if "$do_copy"; then
    if "$DRY_RUN"; then
      summary=$(diff_change_summary "$dest_file" "$TMP_NORM" "$src_file" "$rel")
      echo -e "${YELLOW}[would copy]${NC} $rel${summary}"
    else
      summary=""
      if $VERBOSE; then
        summary=$(diff_change_summary "$dest_file" "$TMP_NORM" "$src_file" "$rel")
      fi
      mkdir -p "$(dirname "$dest_file")"
      cp -p "$src_file" "$dest_file"
      if is_text_for_replace "$rel"; then
        apply_replace "$dest_file"
        ((REPLACED++)) || true
      fi
      $VERBOSE && echo -e "${GREEN}[copied]${NC} $rel${summary}"
    fi
    ((COPIED++)) || true
  fi
done < "$TMP_SRC"

# 2) Files only in dest: report and handle
ONLY_DEST_COUNT=$(wc -l < "$TMP_ONLY_DEST" | tr -d ' ')
if [[ "$ONLY_DEST_COUNT" -gt 0 ]]; then
  echo ""
  echo -e "${YELLOW}Files only in dest ($ONLY_DEST_COUNT):${NC}"
  cat "$TMP_ONLY_DEST" | while read -r rel; do echo "  $rel"; done
  echo ""

  ACTION="$NEW_IN_DEST"
  if [[ -z "$ACTION" ]]; then
    if [[ -t 0 ]]; then
      echo "What to do? [c]opy to source, [i]gnore, [d]elete from dest (default: i)"
      read -r answer || true
      case "${answer:-i}" in
        c|copy)   ACTION=copy ;;
        i|ignore) ACTION=ignore ;;
        d|delete) ACTION=delete ;;
        *)        ACTION=ignore ;;
      esac
    else
      echo "No TTY and --new-in-dest not set; defaulting to ignore."
      ACTION=ignore
    fi
  fi

  case "$ACTION" in
    copy)
      while IFS= read -r rel; do
        dest_file="$DEST/$rel"
        src_file="$SOURCE/$rel"
        if "$DRY_RUN"; then
          echo -e "${YELLOW}[would copy to source]${NC} $rel"
        else
          mkdir -p "$(dirname "$src_file")"
          cp -p "$dest_file" "$src_file"
          # Reverse replace so Agentize keeps github.com/ghiac/agentize imports
          apply_replace "$src_file" "$REPLACE_TO" "$REPLACE_FROM"
          $VERBOSE && echo -e "${GREEN}[copied to source]${NC} $rel"
        fi
      done < "$TMP_ONLY_DEST"
      ;;
    delete)
      while IFS= read -r rel; do
        dest_file="$DEST/$rel"
        if "$DRY_RUN"; then
          echo -e "${YELLOW}[would delete]${NC} $rel"
        else
          rm -f "$dest_file"
          $VERBOSE && echo -e "${GREEN}[deleted]${NC} $rel"
        fi
      done < "$TMP_ONLY_DEST"
      ;;
    ignore|*)
      echo "Skipping (ignore)."
      ;;
  esac
fi

echo ""
echo -e "${GREEN}Done.${NC} Copied/updated: $COPIED (import path replaced in $REPLACED text files). Skipped (import-only): $SKIPPED. Only-in-dest: $ONLY_DEST_COUNT."
