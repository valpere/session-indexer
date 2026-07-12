---
name: generate-find-bugs
description: "Generates a project-specific /find-bugs skill with stack-specific security and correctness checklists. Run once per project. Usage: /generate-find-bugs"
---

# Skill: /generate-find-bugs
# Generate Project-Specific /find-bugs Skill

Detects the project's tech stack and appends language-specific security and correctness
checklists to the generic `/find-bugs` skill.

---

## DISCOVERY STEPS

### Step 1 — Detect stack

```bash
ls package.json go.mod requirements.txt Cargo.toml 2>/dev/null | head -5
```

### Step 2 — Read CLAUDE.md architecture section

```bash
cat CLAUDE.md 2>/dev/null | head -80
```

### Step 3 — Find existing fragile areas

```bash
grep -rn "TODO\|FIXME\|HACK\|unsafe" . \
  --include="*.go" --include="*.ts" --include="*.tsx" \
  --exclude-dir=node_modules --exclude-dir=.git \
  2>/dev/null | head -15
```

---

## OUTPUT

Write to `.claude/skills/find-bugs/SKILL.md`.

Keep Phases 1–5 and the output format intact. Add a **Stack-Specific Checklists** section
after Phase 3, tailored to detected stack:

**Go:**
```markdown
### Go Checklist
- [ ] Goroutine leaks — all goroutines bounded by context or timeout?
- [ ] Nil interface vs nil pointer — returning interface wrapping nil concrete?
- [ ] Context propagation — request contexts passed to all blocking operations?
- [ ] Mutex scope — mutex unlocked in the same scope it was locked?
- [ ] SSE headers — WriteHeader called exactly once, before any body writes?
- [ ] Request size limits — applied before decoding JSON from untrusted input?
- [ ] Response body close — deferred after nil check?
```

**React/TypeScript:**
```markdown
### React / TypeScript Checklist
- [ ] Stale closures — useEffect captures variables that change? All in deps array?
- [ ] Unmount safety — async callbacks check if component still mounted before setState?
- [ ] Missing key props — every .map() rendering JSX has unique stable key?
- [ ] Direct state mutation — objects/arrays mutated in place instead of new refs?
- [ ] XSS via dangerouslySetInnerHTML — LLM or user content injected unsanitized?
- [ ] CORS bypass — origin validated by exact match, not prefix/substring?
```

**Node/Express:**
```markdown
### Node / Express Checklist
- [ ] Dynamic code execution — user-supplied strings reaching code evaluation APIs?
- [ ] Path traversal — file paths built from user input without resolve + prefix check?
- [ ] Prototype pollution — parsed JSON merged into objects without sanitization?
- [ ] ReDoS — regex applied to user input that could catastrophically backtrack?
```

Also add project-specific attack surface items from Steps 2–3.

---

## RULES

- Write to `.claude/skills/find-bugs/SKILL.md` — overwrite generic.
- Keep all 5 generic phases intact.
- After writing, confirm: "Wrote project-specific /find-bugs. Stack: {stack}."
