#!/usr/bin/env bash
# loom - Execute a Claude Code workflow from a YAML DAG definition
#
# Usage: loom.sh <workflow.yaml> [options]
#   -m, --model <model>    Override model (e.g. sonnet, opus)
#   -e, --effort <level>   Override effort (low, medium, high, max)
#   -d, --dry-run          Print resolved execution plan without running
#   -v, --verbose         Show full claude output live (default: captured)
#   -h, --help            Show this help

set -euo pipefail

# ── helpers ──────────────────────────────────────────────────────────────────

die()  { echo "ERROR: $*" >&2; exit 1; }
info() { echo "[loom] $*"; }
ok()   { echo "[loom] ✓ $*"; }

usage() {
  grep '^#' "$0" | grep -v '#!/' | sed 's/^# \{0,1\}//'
  exit 0
}

require() {
  for cmd in "$@"; do
    command -v "$cmd" &>/dev/null || die "Required command not found: $cmd"
  done
}

# ── arg parsing ──────────────────────────────────────────────────────────────

YAML_FILE=""
MODEL_OVERRIDE=""
EFFORT_OVERRIDE=""
DRY_RUN=0
VERBOSE=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help)    usage ;;
    -d|--dry-run) DRY_RUN=1; shift ;;
    -v|--verbose) VERBOSE=1; shift ;;
    -m|--model)   MODEL_OVERRIDE="$2"; shift 2 ;;
    -e|--effort)  EFFORT_OVERRIDE="$2"; shift 2 ;;
    -*)           die "Unknown option: $1" ;;
    *)
      [[ -z "$YAML_FILE" ]] || die "Unexpected argument: $1"
      YAML_FILE="$1"; shift ;;
  esac
done

[[ -n "$YAML_FILE" ]] || die "No workflow YAML provided. Usage: loom.sh <workflow.yaml>"
[[ -f "$YAML_FILE" ]] || die "File not found: $YAML_FILE"

require claude python3

# ── parse YAML + topological sort via inline python ──────────────────────────
#
# Outputs one JSON line per task in execution order:
# {"id":"..","prompt":"..","output_file":"..","context_from":[..]}

PLAN_JSON=$(python3 - "$YAML_FILE" <<'PYEOF'
import sys, json

yaml_file = sys.argv[1]

# ---- minimal YAML parser (no PyYAML dependency) ----------------------------
# Handles the subset used in workflow files: mappings, sequences, scalars.
# Multi-line block scalars (|) are supported for prompts.

def parse_yaml(path):
    with open(path) as f:
        lines = f.readlines()
    return parse_value(lines, 0, 0)[0]

def strip_comment(s):
    # very naive: don't strip '#' inside quotes
    in_q = False
    qc = ''
    for i, c in enumerate(s):
        if in_q:
            if c == qc: in_q = False
        elif c in ('"', "'"):
            in_q, qc = True, c
        elif c == '#':
            return s[:i]
    return s

def parse_value(lines, idx, base_indent):
    """Return (value, next_idx)."""
    # skip blank and comment lines before deciding type
    while idx < len(lines):
        l = lines[idx]
        if l.strip() == '' or l.strip().startswith('#'):
            idx += 1
            continue
        break
    if idx >= len(lines):
        return None, idx

    line = lines[idx]
    stripped = line.lstrip()

    # block literal scalar  |
    if stripped.rstrip('\n') == '|' or stripped.rstrip('\n').endswith(': |'):
        return parse_block_literal(lines, idx + 1, base_indent + 2)

    # sequence
    if stripped.startswith('- ') or stripped.rstrip('\n') == '-':
        return parse_sequence(lines, idx, base_indent)

    # mapping (key: ...)
    return parse_mapping(lines, idx, base_indent)

def current_indent(line):
    return len(line) - len(line.lstrip())

def parse_block_literal(lines, idx, min_indent):
    """Collect indented text block."""
    block_indent = None
    parts = []
    while idx < len(lines):
        line = lines[idx]
        if line.strip() == '':
            parts.append('\n')
            idx += 1
            continue
        ind = current_indent(line)
        if block_indent is None:
            block_indent = ind
        if ind < block_indent:
            break
        parts.append(line[block_indent:])
        idx += 1
    return ''.join(parts).rstrip('\n'), idx

def parse_sequence(lines, idx, base_indent):
    result = []
    while idx < len(lines):
        line = lines[idx]
        if line.strip() == '' or line.strip().startswith('#'):
            idx += 1
            continue
        ind = current_indent(line)
        if ind < base_indent:
            break
        stripped = line.lstrip()
        if not stripped.startswith('- ') and stripped.rstrip('\n') != '-':
            break
        rest = stripped[2:].rstrip('\n').strip()
        # bare dash: value is entirely on next lines
        if not rest:
            idx += 1
            val, idx = parse_value(lines, idx, ind + 2)
            result.append(val)
        # inline scalar (no colon → not a mapping key)
        elif ':' not in rest and not rest.startswith('{'):
            result.append(parse_scalar(rest))
            idx += 1
        else:
            # "- key: value" — synthesize a virtual line so parse_mapping
            # sees the first key, then continues with following lines
            virtual = ' ' * (ind + 2) + rest + '\n'
            synthetic = [virtual] + lines[idx + 1:]
            val, consumed = parse_mapping(synthetic, 0, ind + 2)
            # consumed lines in synthetic = lines consumed after virtual line
            idx = (idx + 1) + (consumed - 1)
            result.append(val)
    return result, idx

def parse_mapping(lines, idx, base_indent):
    result = {}
    while idx < len(lines):
        line = lines[idx]
        if line.strip() == '' or line.strip().startswith('#'):
            idx += 1
            continue
        ind = current_indent(line)
        if ind < base_indent:
            break
        stripped = strip_comment(line.lstrip()).rstrip('\n')
        if not stripped or stripped.startswith('-'):
            break
        if ':' not in stripped:
            idx += 1
            continue
        colon = stripped.index(':')
        key = stripped[:colon].strip()
        val_raw = stripped[colon+1:].strip()

        if val_raw == '|':
            idx += 1
            val, idx = parse_block_literal(lines, idx, ind + 2)
            result[key] = val
        elif val_raw == '':
            # value is on next line(s)
            idx += 1
            val, idx = parse_value(lines, idx, ind + 2)
            result[key] = val
        else:
            # inline scalar or inline list [a, b]
            if val_raw.startswith('[') and val_raw.endswith(']'):
                inner = val_raw[1:-1]
                result[key] = [parse_scalar(x.strip()) for x in inner.split(',') if x.strip()]
            else:
                result[key] = parse_scalar(val_raw)
            idx += 1
    return result, idx

def parse_scalar(s):
    s = s.strip().strip('"').strip("'")
    if s.lower() == 'true':  return True
    if s.lower() == 'false': return False
    try: return int(s)
    except ValueError: pass
    try: return float(s)
    except ValueError: pass
    return s

# ---- load and validate -------------------------------------------------------

data, _ = parse_mapping(open(yaml_file).readlines(), 0, 0)

tasks_raw = data.get('tasks', [])
if not tasks_raw:
    print(json.dumps({"error": "No tasks defined in workflow"}))
    sys.exit(1)

tasks = {}
for t in tasks_raw:
    tid = t.get('id', '')
    if not tid:
        print(json.dumps({"error": "Task missing 'id'"}))
        sys.exit(1)
    prompt       = t.get('prompt', '')
    context_from = t.get('context_from') or []
    depends_on   = list(t.get('depends_on') or [])
    # {{task_id}} placeholders in prompt imply both context_from and depends_on
    import re
    for placeholder_id in re.findall(r'\{\{(\w+)\}\}', prompt):
        if placeholder_id not in context_from:
            context_from.append(placeholder_id)
        if placeholder_id not in depends_on:
            depends_on.append(placeholder_id)
    # context_from (explicit or via placeholder) implies a dependency
    for cid in context_from:
        if cid not in depends_on:
            depends_on.append(cid)
    tasks[tid] = {
        'id':           tid,
        'prompt':       prompt,
        'description':  t.get('description', ''),
        'model':        t.get('model', ''),
        'effort':       t.get('effort', ''),
        'depends_on':   depends_on,
        'context_from': context_from,
    }
    if not tasks[tid]['prompt']:
        print(json.dumps({"error": f"Task '{tid}' has no prompt"}))
        sys.exit(1)

# ---- topological sort (Kahn's algorithm) ------------------------------------

in_degree = {tid: 0 for tid in tasks}
adj       = {tid: [] for tid in tasks}

for tid, task in tasks.items():
    for dep in task['depends_on']:
        if dep not in tasks:
            print(json.dumps({"error": f"Task '{tid}' depends on unknown task '{dep}'"}))
            sys.exit(1)
        adj[dep].append(tid)
        in_degree[tid] += 1

queue  = [tid for tid, deg in in_degree.items() if deg == 0]
order  = []

while queue:
    queue.sort()  # deterministic ordering among independent tasks
    node = queue.pop(0)
    order.append(node)
    for neighbour in adj[node]:
        in_degree[neighbour] -= 1
        if in_degree[neighbour] == 0:
            queue.append(neighbour)

if len(order) != len(tasks):
    print(json.dumps({"error": "Cycle detected in workflow DAG"}))
    sys.exit(1)

# ---- emit plan ---------------------------------------------------------------

meta = {
    'name':          data.get('name', 'unnamed'),
    'model':         data.get('model', ''),
    'effort':        data.get('effort', ''),
    'system_prompt': data.get('system_prompt', ''),
}
print(json.dumps({'meta': meta, 'order': order, 'tasks': tasks}))
PYEOF
)

# Check for python-level errors
if echo "$PLAN_JSON" | python3 -c "import sys,json; d=json.load(sys.stdin); sys.exit(0 if 'error' not in d else 1)" 2>/dev/null; then
  :
else
  ERR=$(echo "$PLAN_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin).get('error','unknown'))")
  die "Workflow parse error: $ERR"
fi

# ── extract plan fields ───────────────────────────────────────────────────────

_jq() { echo "$PLAN_JSON" | python3 -c "import sys,json; d=json.load(sys.stdin); print($1)"; }

WORKFLOW_NAME=$(_jq "d['meta']['name']")
YAML_MODEL=$(_jq "d['meta'].get('model','')")
YAML_EFFORT=$(_jq "d['meta'].get('effort','')")
SYSTEM_PROMPT=$(_jq "d['meta'].get('system_prompt','')")
TASK_ORDER=$(_jq "' '.join(d['order'])")

TASK_COUNT=$(echo "$TASK_ORDER" | wc -w | tr -d ' ')

# ── dry-run: print plan ───────────────────────────────────────────────────────

print_plan() {
  info "Workflow : $WORKFLOW_NAME"
  info "Model    : ${MODEL_OVERRIDE:-${YAML_MODEL:-sonnet}}"
  info "Effort   : ${EFFORT_OVERRIDE:-${YAML_EFFORT:--}}"
  info "Tasks ($TASK_COUNT) in execution order:"
  local i=1
  for tid in $TASK_ORDER; do
    local DESC DEPS TMODEL TEFFORT
    DESC=$(echo "$PLAN_JSON"   | python3 -c "import sys,json; t=json.load(sys.stdin)['tasks']['$tid']; print(t['description'] or t['prompt'][:60]+'...')")
    DEPS=$(echo "$PLAN_JSON"   | python3 -c "import sys,json; print(', '.join(json.load(sys.stdin)['tasks']['$tid']['depends_on']) or 'none')")
    TMODEL=$(echo "$PLAN_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin)['tasks']['$tid']['model'] or '-')")
    TEFFORT=$(echo "$PLAN_JSON"| python3 -c "import sys,json; print(json.load(sys.stdin)['tasks']['$tid']['effort'] or '-')")
    printf "  %2d. %-20s  model=%-10s  effort=%-8s  deps=%s\n" "$i" "$tid" "$TMODEL" "$TEFFORT" "$DEPS"
    (( i++ ))
  done
}

if [[ $DRY_RUN -eq 1 ]]; then
  print_plan
  exit 0
fi

# ── execution ─────────────────────────────────────────────────────────────────

CONTEXT_DIR=$(mktemp -d)
trap 'rm -rf "$CONTEXT_DIR"' EXIT

print_plan
echo ""

TOTAL_COST=0
TOTAL_IN=0
TOTAL_OUT=0
TOTAL_CACHE_READ=0

STEP=0
for tid in $TASK_ORDER; do
  (( STEP++ ))

  # ---- task metadata
  TASK_JSON=$(echo "$PLAN_JSON" | python3 -c "
import sys, json
d = json.load(sys.stdin)
t = d['tasks']['$tid']
print(json.dumps(t))
")

  PROMPT=$(echo "$TASK_JSON"      | python3 -c "import sys,json; print(json.load(sys.stdin)['prompt'])")
  DESC=$(echo "$TASK_JSON"        | python3 -c "import sys,json; t=json.load(sys.stdin); print(t['description'] or t['id'])")
  CONTEXT_IDS=$(echo "$TASK_JSON" | python3 -c "import sys,json; print(' '.join(json.load(sys.stdin)['context_from']))")
  TASK_MODEL=$(echo "$TASK_JSON"  | python3 -c "import sys,json; print(json.load(sys.stdin)['model'])")
  TASK_EFFORT=$(echo "$TASK_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin)['effort'])")
  # fallback chain: task > CLI override > workflow > default
  RESOLVED_MODEL="${TASK_MODEL:-${MODEL_OVERRIDE:-${YAML_MODEL:-sonnet}}}"
  RESOLVED_EFFORT="${TASK_EFFORT:-${EFFORT_OVERRIDE:-${YAML_EFFORT:-}}}"

  # ---- build full prompt (inject context from prior tasks)
  FULL_PROMPT="$PROMPT"

  for ctx_id in $CONTEXT_IDS; do
    CTX_FILE="$CONTEXT_DIR/$ctx_id.out"
    if [[ -f "$CTX_FILE" ]]; then
      CTX_CONTENT=$(cat "$CTX_FILE")
      # Replace {{ctx_id}} placeholder if present; otherwise append at end
      if echo "$FULL_PROMPT" | grep -qF "{{$ctx_id}}"; then
        FULL_PROMPT="${FULL_PROMPT//"{{$ctx_id}}"/"$CTX_CONTENT"}"
      else
        FULL_PROMPT=$(printf '%s\n\n--- Output from task "%s" ---\n%s\n--- end ---' \
          "$FULL_PROMPT" "$ctx_id" "$CTX_CONTENT")
      fi
    else
      echo "  WARN: context_from '$ctx_id' has no captured output, skipping" >&2
    fi
  done

  info "[$STEP/$TASK_COUNT] $tid — $DESC (model: $RESOLVED_MODEL, effort: ${RESOLVED_EFFORT:--})"

  # ---- build claude command
  CLAUDE_ARGS=(-p --output-format json --model "$RESOLVED_MODEL")
  [[ -n "$RESOLVED_EFFORT" ]] && CLAUDE_ARGS+=(--effort "$RESOLVED_EFFORT")
  [[ -n "$SYSTEM_PROMPT" ]]   && CLAUDE_ARGS+=(--system-prompt "$SYSTEM_PROMPT")
  CLAUDE_ARGS+=(--dangerously-skip-permissions)

  # ---- run claude
  RAW_OUT_FILE="$CONTEXT_DIR/$tid.json"
  TASK_OUT_FILE="$CONTEXT_DIR/$tid.out"

  if [[ $VERBOSE -eq 1 ]]; then
    claude "${CLAUDE_ARGS[@]}" "$FULL_PROMPT" | tee "$RAW_OUT_FILE"
  else
    # Run in background and show spinner with elapsed time while waiting
    claude "${CLAUDE_ARGS[@]}" "$FULL_PROMPT" > "$RAW_OUT_FILE" 2>&1 &
    CLAUDE_PID=$!
    SPIN='⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏'
    SPIN_I=0
    START_S=$SECONDS
    while kill -0 "$CLAUDE_PID" 2>/dev/null; do
      ELAPSED=$(( SECONDS - START_S ))
      printf "\r  %s  %ds" "${SPIN:SPIN_I%${#SPIN}:1}" "$ELAPSED"
      SPIN_I=$(( SPIN_I + 1 ))
      sleep 0.1
    done
    printf "\r  done (%ds)\n" "$(( SECONDS - START_S ))"
    wait "$CLAUDE_PID" || { echo "  ERROR: claude exited with error" >&2; cat "$RAW_OUT_FILE" >&2; exit 1; }
  fi

  # ---- parse JSON envelope: extract result text + usage stats
  read -r TASK_COST TASK_IN TASK_OUT TASK_CACHE_READ < <(python3 -c "
import sys, json
d = json.load(open('$RAW_OUT_FILE'))
u = d.get('usage', {})
print(
  d.get('total_cost_usd', 0),
  u.get('input_tokens', 0),
  u.get('output_tokens', 0),
  u.get('cache_read_input_tokens', 0),
)
")
  python3 -c "import sys,json; print(json.load(open('$RAW_OUT_FILE')).get('result',''))" > "$TASK_OUT_FILE"

  printf "  tokens: %s in / %s out / %s cache-read  cost: \$%.6f\n" \
    "$TASK_IN" "$TASK_OUT" "$TASK_CACHE_READ" "$TASK_COST"

  # accumulate totals
  TOTAL_COST=$(python3 -c "print($TOTAL_COST + $TASK_COST)")
  TOTAL_IN=$(( TOTAL_IN + TASK_IN ))
  TOTAL_OUT=$(( TOTAL_OUT + TASK_OUT ))
  TOTAL_CACHE_READ=$(( TOTAL_CACHE_READ + TASK_CACHE_READ ))

  echo ""
done

echo "────────────────────────────────────────"
printf "  total tokens : %s in / %s out / %s cache-read\n" \
  "$TOTAL_IN" "$TOTAL_OUT" "$TOTAL_CACHE_READ"
printf "  total cost   : \$%.6f\n" "$TOTAL_COST"
echo "────────────────────────────────────────"
ok "Workflow '$WORKFLOW_NAME' complete."
