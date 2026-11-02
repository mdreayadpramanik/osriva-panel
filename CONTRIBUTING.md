# Contributing to OSRIVA Panel

Thank you for helping. OSRIVA Panel is AGPL-3.0 and accepts contributions under
the same license (inbound = outbound). There is no CLA. Sign off your commits
(`git commit -s`) to certify the [Developer Certificate of Origin](https://developercertificate.org/).

## Ground rules

1. **No fake functionality.** Never add UI for a feature the backend does not
   implement, never show placeholder statistics, and never report success without verifying it.
2. **No billing.** Payment, invoice, subscription and pricing code is out of scope, permanently.
3. **Tests with changes.** Backend changes need unit tests. Store changes must
   extend `store/storetest` so that every implementation stays consistent.
4. **Migrations are forward-only.** Never edit a released migration (ADR-0003).
5. **Security first.** Read SECURITY.md. Privileged host operations go through the agent only.
6. Keep `DEVELOPMENT_STATUS.md`, `TASKS.md`, `COMPATIBILITY_MATRIX.md` and
   `ARCHITECTURE.md` accurate in the same pull request.

## Checks before opening a PR

```bash
gofmt -l .
```

```bash
go vet ./...
```

```bash
go test ./...
```

```bash
npm run typecheck
```

```bash
npm run build:dashboard
```
