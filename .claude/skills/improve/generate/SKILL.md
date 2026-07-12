---
name: generate-improve
description: "Generates a project-specific /improve skill with actual architecture layers, reference files, and known constraints from CLAUDE.md. Run once per project. Usage: /generate-improve"
---

# Skill: /generate-improve
# Generate Project-Specific /improve Skill

Reads the project's architecture documentation and writes a project-specific `/improve`
skill that knows the actual layer boundaries, reference files, and known intentional
deviations.

---

## DISCOVERY STEPS

### Step 1 — Read architecture docs

```bash
cat CLAUDE.md 2>/dev/null
ls docs/ 2>/dev/null
cat docs/architecture.md 2>/dev/null || cat docs/ARCHITECTURE.md 2>/dev/null | head -80
```

### Step 2 — Map layer structure

From CLAUDE.md and docs, extract:
- Layer names and their order (e.g., UI → Hooks → Services → DB)
- Package/module boundaries
- Which layer can import which
- Intentional rule exceptions documented in CLAUDE.md

### Step 3 — Find key reference files

```bash
ls internal/ src/ app/ lib/ 2>/dev/null | head -20
# Find interface files
find . -name "interfaces.go" -o -name "types.ts" -o -name "contracts.ts" 2>/dev/null \
  | grep -v node_modules | head -5
```

### Step 4 — Find proposal/decision files

```bash
ls .proposals.md docs/decisions/ .claude/plans/ 2>/dev/null | head -5
```

---

## OUTPUT

Write to `.claude/skills/improve/SKILL.md`.

Keep the overall procedure intact. Replace or augment:

1. **Step 2 Research — Internal first**: replace generic file list with actual project files:
   ```
   - Read CLAUDE.md for architecture constraints
   - Read [actual architecture doc path]
   - Read [actual interfaces file path] if touching [relevant layer]
   - Read [actual proposal/decision file path]
   ```

2. **Step 3A — Architecture alignment**: replace generic layer names with actual layers:
   ```
   - Does this respect [Layer1] → [Layer2] → [Layer3] boundaries?
   - Layer N rule: [specific constraint from CLAUDE.md]
   - Known intentional exceptions: [list from CLAUDE.md]
   ```

3. **Frontmatter description**: update to include project name.

---

## RULES

- Write to `.claude/skills/improve/SKILL.md` — overwrite generic.
- Keep scoring matrix and verdict thresholds unchanged.
- Document intentional exceptions explicitly — don't critique them as flaws.
- After writing, confirm: "Wrote project-specific /improve for {project}."
