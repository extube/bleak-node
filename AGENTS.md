# AGENTS.md

Rules for AI agents and contributors working in the **bleak-node** repository.

## Git Flow Rules

The workflow is based on a `dev` branch with feature (`feat`) and bugfix (`bug`) branches.

### Branches

| Rule | Detail |
| --- | --- |
| Don't push `main` | Never push to the `main` branch. |
| Don't merge into `main` | Never merge any branch into `main`. |
| Don't commit to `dev` | Never commit directly on the `dev` branch. |
| Pushing `dev` is allowed | You may push the `dev` branch. |
| Merging into `dev` is allowed | You may merge `feat` and `bug` branches into `dev`. |

### Branch naming

Branch names **must** follow this format:

```
<feat/bug>/#<number-of-feature>-<description>
```

Examples:

```
feat/#12-user-authentication
bug/#34-fix-db-connection-leak
```

Where:

- `<feat/bug>` — branch type: `feat` for new features, `bug` for bug fixes
- `<number-of-feature>` — the issue/feature number (prefixed with `#`)
- `<description>` — a short, lowercase, hyphen-separated description
