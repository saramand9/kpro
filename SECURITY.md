# Security Policy

KPro is pre-1.0. Security fixes are developed on `main` and released on the
latest published minor line when applicable. Once a new minor line is
published, older minor lines and superseded patches are not maintained as
separate security lines. Upgrade using the matching release notes.

| Version | Supported |
| --- | --- |
| latest patch of the latest published minor line | yes |
| older minor lines, superseded patches, and unreviewed commits | no |

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use the repository's
GitHub **Security** tab to submit a private vulnerability report. Include the
affected commit, configuration, reproduction steps, expected impact, and any
known workaround.

If private vulnerability reporting is unavailable, contact the repository
owner privately before publishing details. Acknowledgement and remediation
timelines depend on severity and reproducibility.

## Deployment responsibility

Before deployment, run the full test and race suites plus `govulncheck ./...`,
pin an exact reviewed release (or a reviewed commit for an unreleased fix), and
review the production-readiness checklist.
Authentication, authorization, secrets, TLS certificate management, and
network policy remain application or platform responsibilities.
