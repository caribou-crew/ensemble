# templates/company-stack

A starting point for sharing an ensemble setup across a team, so the second
person to set up your local stack spends five minutes instead of thirty.

`ensemble.yaml` is topology-agnostic — it doesn't know or care how the
services on disk got there. This template fills that gap with one file
(`repos.txt`) that says where each service actually lives, and one script
(`setup.sh`) that reads it.

## The pattern

Copy this folder into its own repo (e.g. `github.com/acme/local-stack`),
rename it, and fill in your company's real repos and branches:

```
acme-local-stack/
├── ensemble.yaml   # topology: services, ports, health checks, stubs
├── repos.txt       # where each service's `dir:` actually comes from
└── setup.sh        # clones/updates everything repos.txt lists
```

Once it exists, onboarding a teammate is:

```sh
git clone github.com/acme/local-stack
cd local-stack
./setup.sh          # clones every service repo into ./services/*
ensemble up          # starts the stack, serves the dashboard
```

Whoever sets up the stack first pays the cost of picking ports, health
endpoints, and stub responses once. Everyone after them just runs two
commands.

## Keeping `repos.txt` and `ensemble.yaml` in sync

Each line in `repos.txt` is `name url branch dir`. The `dir` must match the
`dir:` of the same service in `ensemble.yaml` — that's the only thing tying
the two files together, there's no validation beyond it being a normal path.
`setup.sh` is safe to re-run any time; it fetches and fast-forwards services
that are already cloned instead of re-cloning them.

## What this isn't

This is a convention, not an ensemble feature — `ensemble` itself has no
opinion about where your service directories came from, and doesn't run any
git commands on your behalf. Keep company-specific repo URLs, branches, and
auth entirely in your own stack repo.
