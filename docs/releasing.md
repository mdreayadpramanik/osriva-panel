# Releases and how to verify them

Releases are built by `.github/workflows/release.yml`.

* **Files:** agent builds for nine platforms, the API for linux/amd64 and linux/arm64,
  the `osriva-plugin` signing tool, the installer, and `SHA256SUMS`.
* **Signature:** `SHA256SUMS` is signed with **Sigstore keyless signing**. The workflow's
  GitHub Actions OIDC identity gets a short-lived certificate, and the signature is
  recorded in Sigstore's public transparency log (Rekor). No long-lived signing key
  exists that could leak.
* **Container images** (tags only) are pushed to GHCR and signed the same way.

## Published releases

| Version | Date | Notes |
|---------|------|-------|
| [v1.0.0](https://github.com/mdreayadpramanik/osriva-panel/releases/tag/v1.0.0) | 2026-11-05 | First **full release**, on the owner's decision. Released without an independent security review or a soak test on real hosts; what is and is not tested is in DEVELOPMENT_STATUS.md. Release run 42024214429 (tag on `33ec22e`, CI 42022820862). 24 files, marked latest. Checked independently with cosign 2.4.1 on Windows 11: signature `Verified OK` for the exact v1.0.0 workflow identity, every downloaded file matches `SHA256SUMS`, a tampered checksum list refused, the licence report has no `UNKNOWN` entry. Images `ghcr.io/mdreayadpramanik/osriva-panel/{api,dashboard}:1.0.0` are signed and still private. |
| [v0.1.0](https://github.com/mdreayadpramanik/osriva-panel/releases/tag/v0.1.0) | 2026-11-04 | First **pre-release**, not production-ready. Release run 41846227536. Signature and checksums verified independently with cosign 2.4.1 on Windows 11. The images `ghcr.io/mdreayadpramanik/osriva-panel/{api,dashboard}:0.1.0` are signed (Rekor entries 3690534729 and 3690535048) but still **private** on GHCR, so pulling them needs a GitHub login until the owner makes the packages public. |

## Verifying a download

Install [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) 2.x, then,
in the directory with the downloaded files, use the exact identity of the version you
downloaded (here v0.1.0):

```sh
cosign verify-blob \
  --bundle SHA256SUMS.sigstore.json \
  --certificate-identity https://github.com/mdreayadpramanik/osriva-panel/.github/workflows/release.yml@refs/tags/v0.1.0 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
```

The first command proves that `SHA256SUMS` was signed by this repository's release
workflow running for that tag. The second checks your files against it. If either fails,
do not use the files.

To accept any release tag instead of one version, replace `--certificate-identity …`
with:

```sh
--certificate-identity-regexp '^https://github.com/mdreayadpramanik/osriva-panel/.github/workflows/release.yml@refs/tags/v'
```

Keep this pattern free of backslash escapes. In testing on Windows (Git Bash), the
escaped form `\.` did not match the certificate and verification failed, although the
signature was correct.

A container image can be verified the same way, once you can pull it:

```sh
cosign verify ghcr.io/mdreayadpramanik/osriva-panel/api:0.1.0 \
  --certificate-identity https://github.com/mdreayadpramanik/osriva-panel/.github/workflows/release.yml@refs/tags/v0.1.0 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Making a release (maintainers)

1. Make sure `main` is green (`CI` and `System tests`) and `DEVELOPMENT_STATUS.md` is
   current.
2. Run a dry run: **Actions → Release → Run workflow**, or
   `gh workflow run release.yml -f version=<x.y.z-rc>`.
3. Tag and push: `git tag -a v<x.y.z> -m … && git push origin v<x.y.z>`. The workflow
   builds, signs and verifies again, pushes and signs the images, and creates a GitHub
   **pre-release**.
4. New GHCR packages start out private. Make them public under the package settings on
   GitHub if anyone should be able to pull them.

Tags with a suffix (for example `v1.1.0-rc1`) are published as pre-releases, plain
version tags (`v1.0.0`) as full releases. v1.0.0 was published as a full release on the
owner's decision, before an independent security review and before a soak test on
real hosts (docs/production-checklist.md lists both as still open).
