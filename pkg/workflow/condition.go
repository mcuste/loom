package workflow

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Condition is a parsed, validated `when:` expression. It is produced by
// ParseCondition at load time and evaluated by the executor once a task's
// dependencies resolve. The zero value is not usable; obtain one via
// ParseCondition.
//
// The expression grammar is deliberately tiny. An expression is exactly one
// of:
//
//   - a scalar comparison of a task-output placeholder against a literal:
//     `{{id}} == "x"`, `{{id}} != "x"`, or a numeric `{{id}} < n` / `{{id}} > n`
//     where the referenced task's output parses as an integer;
//   - a `contains({{id}}, "substr")` substring test;
//   - a `succeeded(id)` / `failed(id)` status helper.
type Condition struct {
	// raw is the original expression text, preserved for diagnostics.
	raw string
	// op selects the evaluation rule: one of "==", "!=", "<", ">", "contains",
	// "succeeded", or "failed".
	op string
	// ref is the task id read from Env at evaluation time.
	ref TaskID
	// literal holds the right-hand string for "==", "!=", and "contains".
	literal string
	// num holds the right-hand integer for "<" and ">".
	num int
}

// Env supplies the runtime values a Condition is evaluated against. Outputs
// maps each task id to its produced output; Succeeded records, per task id,
// whether that task ran to completion successfully; Skipped records, per task
// id, whether that task was skipped by its own `when:` guard. The three
// dispositions are distinct: `succeeded(id)` reads Succeeded[id]; `failed(id)`
// is true only when the task neither succeeded nor was skipped, so a skipped
// task is never reported as failed; placeholder and contains operands read
// Outputs[id].
type Env struct {
	Outputs   map[TaskID]string
	Succeeded map[TaskID]bool
	Skipped   map[TaskID]bool
}

var (
	// comparisonRe matches `{{id}} <op> <rhs>` for op in ==, !=, <, >. The rhs
	// is captured verbatim and validated per-operator (quoted string for ==/!=,
	// integer for </>).
	comparisonRe = regexp.MustCompile(`^\{\{(` + identifierClass + `)\}\}\s*(==|!=|<|>)\s*(.+)$`)
	// containsRe matches `contains({{id}}, "substr")`.
	containsRe = regexp.MustCompile(`^contains\(\s*\{\{(` + identifierClass + `)\}\}\s*,\s*"((?:[^"\\]|\\.)*)"\s*\)$`)
	// helperRe matches `succeeded(id)` / `failed(id)`.
	helperRe = regexp.MustCompile(`^(succeeded|failed)\(\s*(` + identifierClass + `)\s*\)$`)
	// stringLitRe matches a double-quoted string literal, capturing its inner
	// text.
	stringLitRe = regexp.MustCompile(`^"((?:[^"\\]|\\.)*)"$`)
	// intLitRe matches a (optionally signed) decimal integer literal.
	intLitRe = regexp.MustCompile(`^-?\d+$`)
)

// ParseCondition validates and compiles a `when:` expression. known bounds the
// task ids the expression may reference: a placeholder or status-helper
// argument naming any other id is an UnknownConditionRefError. Callers pass the
// guarded task's dependency set (not the whole workflow) so a reference to a
// task that is not waited on — including the task's own id — is rejected at
// load time. A syntactically invalid expression is a MalformedConditionError.
func ParseCondition(expr string, known map[TaskID]bool) (*Condition, error) {
	raw := expr
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, &MalformedConditionError{Expr: raw, Reason: "empty expression"}
	}

	if m := helperRe.FindStringSubmatch(expr); m != nil {
		ref := TaskID(m[2])
		if !known[ref] {
			return nil, &UnknownConditionRefError{Expr: raw, Ref: string(ref)}
		}
		return &Condition{raw: raw, op: m[1], ref: ref}, nil
	}

	if m := containsRe.FindStringSubmatch(expr); m != nil {
		ref := TaskID(m[1])
		if !known[ref] {
			return nil, &UnknownConditionRefError{Expr: raw, Ref: string(ref)}
		}
		return &Condition{raw: raw, op: "contains", ref: ref, literal: unescapeLiteral(m[2])}, nil
	}

	if m := comparisonRe.FindStringSubmatch(expr); m != nil {
		ref, op, rhs := TaskID(m[1]), m[2], strings.TrimSpace(m[3])
		if !known[ref] {
			return nil, &UnknownConditionRefError{Expr: raw, Ref: string(ref)}
		}
		switch op {
		case "==", "!=":
			lit := stringLitRe.FindStringSubmatch(rhs)
			if lit == nil {
				return nil, &MalformedConditionError{Expr: raw, Reason: fmt.Sprintf("operator %q expects a quoted string", op)}
			}
			return &Condition{raw: raw, op: op, ref: ref, literal: unescapeLiteral(lit[1])}, nil
		case "<", ">":
			if !intLitRe.MatchString(rhs) {
				return nil, &MalformedConditionError{Expr: raw, Reason: fmt.Sprintf("operator %q expects an integer", op)}
			}
			n, err := strconv.Atoi(rhs)
			if err != nil {
				return nil, &MalformedConditionError{Expr: raw, Reason: fmt.Sprintf("invalid integer %q", rhs)}
			}
			return &Condition{raw: raw, op: op, ref: ref, num: n}, nil
		}
	}

	return nil, &MalformedConditionError{Expr: raw, Reason: "does not match any supported form"}
}

// Eval evaluates the condition against env, returning whether the guarded task
// should run. It returns an error when a numeric comparison targets a
// non-integer output.
func (c *Condition) Eval(env Env) (bool, error) {
	switch c.op {
	case "succeeded":
		return env.Succeeded[c.ref], nil
	case "failed":
		// A skipped task is neither succeeded nor failed: report failed only when
		// the task ran and did not succeed, never when it was skipped.
		return !env.Succeeded[c.ref] && !env.Skipped[c.ref], nil
	case "contains":
		return strings.Contains(env.Outputs[c.ref], c.literal), nil
	case "==":
		return env.Outputs[c.ref] == c.literal, nil
	case "!=":
		return env.Outputs[c.ref] != c.literal, nil
	case "<", ">":
		got := env.Outputs[c.ref]
		n, err := strconv.Atoi(strings.TrimSpace(got))
		if err != nil {
			return false, fmt.Errorf("%q: output of %q is not an integer: %q", c.raw, c.ref, got)
		}
		if c.op == "<" {
			return n < c.num, nil
		}
		return n > c.num, nil
	}
	return false, fmt.Errorf("%q: unsupported operator %q", c.raw, c.op)
}

// unescapeLiteral resolves backslash escapes in a string literal captured by
// the condition grammar. The grammar's `\\.` alternative guarantees every
// backslash is followed by some character; this drops the backslash and keeps
// that character verbatim, so `\"` becomes `"` and `\\` becomes `\`. Without
// this, a literal like "say \"hi\"" would retain its backslashes and never
// match the runtime output `say "hi"`.
func unescapeLiteral(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// MalformedConditionError reports a `when:` expression that does not satisfy
// the condition grammar.
type MalformedConditionError struct {
	Expr   string
	Reason string
}

func (e *MalformedConditionError) Error() string {
	return fmt.Sprintf("malformed when expression %q: %s", e.Expr, e.Reason)
}

// UnknownConditionRefError reports a `when:` expression that references a task
// id not present in the workflow.
type UnknownConditionRefError struct {
	Expr string
	Ref  string
}

func (e *UnknownConditionRefError) Error() string {
	return fmt.Sprintf("when expression %q references unknown task %q", e.Expr, e.Ref)
}
