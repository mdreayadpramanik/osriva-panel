# Releases and how to verify them

Releases are built by `.github/workflows/release.yml`.

* **Files:** agent builds for nine platforms, the API for linux/amd64 and linux/arm64,
  the `osriva-plugin` signing tool, the installer, and `SHA256SUMS`.
* **Signature:** `SHA256SUMS` is signed with **Sigstore keyless signing**. The workflow's
  GitHub Actions OIDC identity gets a short-lived certificate, and the signature is
  recorded in Sigstore's public transparency log (Rekor). No long-lived signing key
  exists that could leak.
* **Container images** (tags only) are pushed to GHCR and signed the same way.

Status (2026-11-04): only **dry runs** have been made. They build, sign and verify
everything and keep the files as a workflow artifact, without publishing anything. No
release has been published yet, and the tag path, which pushes images and creates a
GitHub pre-release, has not been run.

## Verifying a download

Install [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) 2.x, then
in the directory with the downloaded files:

```sh
cosign verify-blob \
  --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github.com/mdreayadpramanik/osriva-panel/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
```

The first command proves that `SHA256SUMS` was produced by this repository's release
workflow for a `v*` tag. The second checks your files against it. If either fails, do not
use the files.

A container image (once published) can be verified with:

```sh
cosign verify ghcr.io/mdreayadpramanik/osriva-panel/api:<version> \
  --certificate-identity-regexp '^https://github.com/mdreayadpramanik/osriva-panel/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Making a release (maintainers)

1. Make sure `main` is green (`CI` and `System tests`) and `DEVELOPMENT_STATUS.md` is
   current.
2. Run a dry run: **Actions → Release → Run workflow**, or
   `gh workflow run release.yml -f version=<x.y.z-rc>`.
3. Tag and push: `git tag v<x.y.z> && git push origin v<x.y.z>`. The workflow builds,
   signs and verifies again, pushes and signs the images, and creates a GitHub
   **pre-release**. OSRIVA Panel is not production-ready, so releases stay pre-releases
   until the remaining Phase 14 items are done.
