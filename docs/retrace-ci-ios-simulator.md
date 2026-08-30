# Replaying against a CI iOS Simulator

Symptom: `retrace run` / `retrace replay` binds its listener(s) on
`127.0.0.1` as usual, the same fixtures and app build work fine on a
developer's machine, but on a GitHub-hosted `macos-latest` runner the app
under test logs something like "could not reach http://127.0.0.1:4850/…"
and the listener receives **zero** requests.

## There is no `adb reverse` for the Simulator, and you don't need one

Unlike the Android emulator, the iOS Simulator is not a virtualized guest
with its own network interface — a Simulator app is a real macOS process,
sharing the Mac's actual network stack. `127.0.0.1` inside the app under
test already *is* the runner's loopback, on a CI runner exactly as much as
on a laptop. There is no Simulator equivalent of `adb reverse` to set up,
and none is needed. If you came here looking for that bridge, stop looking:
architecturally it doesn't exist, because it isn't required. Whatever is
breaking your CI run, it is not a missing host↔sim network path.

(retrace's own dual-stack loopback answer — every listener also answers on
its address family's other loopback form, `127.0.0.1` and `::1` — covers a
different, already-solved problem: a client that resolves `localhost` to
the family retrace didn't bind. See the retrace-iterate skill's
"Multiple upstreams" section. That fix already ships; if you're on
`@caribou-crew/retrace@0.0.4` or later and still seeing zero requests
served, address family isn't it either.)

## Start here: which half of the problem do you have?

Before adopting anything below, run these two commands on the runner, with
the Simulator booted and the retrace listener up. They take a minute and
they decide whether the rest of this page applies to you at all.

```sh
# (a) Is the listener actually up and reachable from the runner host?
curl -v http://127.0.0.1:4850/<a-path-the-bundle-recorded>

# (b) Can a NON-app process inside the booted Simulator reach it?
#     Opens the URL in the Simulator's Safari.
xcrun simctl openurl booted http://127.0.0.1:4850/<a-path-the-bundle-recorded>
```

- **(a) fails** — the listener isn't up, isn't on the port you think, or
  isn't running yet when you look. Nothing about iOS is involved. Check
  step ordering: a backgrounded `retrace replay` races the test step that
  follows it, so make the job wait for the port to accept before launching
  the app.
- **(a) succeeds, (b) succeeds, the app still gets nothing** — the network
  path is fine and something *app-specific* is blocking it. That is the
  case the rest of this page is about.
- **(a) succeeds, (b) also fails** — it is **not** the privacy gate, and
  the TCC workaround below will not help you. Something is wrong at the
  job/tooling level. Do not go seed a TCC database; go re-check what the
  job is actually doing.

That last branch is the important one. The workaround at the bottom of this
page is an unofficial hack against an undocumented database, and it is easy
to "apply successfully" while the real cause goes unexamined.

## The leading hypothesis: Local Network privacy, not routing

Since iOS 14, an app must be granted the **Local Network** privacy
permission (`kTCCServiceLocalNetwork`) before some local network
connections succeed; without it, the connection fails immediately rather
than falling back or prompting mid-test. That failure mode matches the
symptom: same app, same fixtures, same address, no requests served.

Two things make this worse for XCUITest specifically, per an open,
Apple-acknowledged bug (Feedback reference used in the public writeup:
r. 73701876 — see [developer.apple.com/forums/thread/676157](https://developer.apple.com/forums/thread/676157)):

- the test-runner process never appears under **Settings > Privacy > Local
  Network** to grant by hand, even interactively, so there's no UI path
  either;
- a fresh CI runner boots a fresh Simulator with nothing granted, and
  there's no interactive session to answer a permission alert even if one
  did appear.

### `NSAllowsLocalNetworking` does not rule this out

If your `Info.plist` already sets `NSAllowsArbitraryLoads` and
`NSAllowsLocalNetworking` and you concluded this class of problem was
eliminated: those are **App Transport Security** keys. ATS governs whether
the app may make *cleartext HTTP* requests to local hosts. The Local
Network privacy permission is a separate TCC gate governing whether the app
may talk to the local network *at all*. They are unrelated mechanisms with
confusingly similar names, and setting the ATS keys has no effect on the
TCC gate. Ruling out ATS does not rule out this.

### How confident to be

Treat this as the leading hypothesis backed by a matching failure mode, not
a confirmed root cause:

- We could not find an authoritative Apple statement pinning down whether a
  plain loopback (`127.0.0.1`) connection is in scope for this permission
  versus only true LAN/Bonjour traffic. Apple's own engineers have left
  forum threads asking exactly this unanswered.
- The tidy story — "it works locally because the grant was approved once on
  your machine" — is not corroborated by the machines we could inspect. A
  dev Mac here with two installed runtimes had **no**
  `kTCCServiceLocalNetwork` rows at all in any Simulator's TCC database:
  no grants, and no denials either. If the gate were being hit and
  satisfied locally, you would expect a row.

Which points at a variable worth eliminating before you touch any database:

### Check the Simulator *runtime version* first

Enforcement behavior around local network access has changed across iOS
Simulator runtimes. "Works locally, fails in CI" is exactly what you would
see if your local Simulator runs an older runtime than the one the runner
image defaults to — and that costs nothing to check:

```sh
xcrun simctl list runtimes        # run locally AND on the runner; compare
```

If they differ, pin the CI destination to the runtime you actually develop
against (`-destination 'platform=iOS Simulator,name=…,OS=<version>'`) and
re-run. If that fixes it, you have a fully supported workaround, a confirmed
explanation, and no unofficial hack to maintain. Try this before the
TCC-seeding below.

### What does *not* help: `simctl privacy`

`xcrun simctl privacy <device> grant <service> <bundle-id>` is the
supported way to pre-grant a permission on a Simulator without a UI, and
it's tempting to reach for it here. It does not have a local-network
service. Checked directly against the Xcode CLI on a current machine:

```
$ xcrun simctl help privacy
	service
	     The service:
	         all - Apply the action to all services.
	         calendar - Allow access to calendar.
	         contacts-limited - Allow access to basic contact info.
	         contacts - Allow access to full contact details.
	         location - Allow access to location services when app is in use.
	         location-always - Allow access to location services at all times.
	         photos-add - Allow adding photos to the photo library.
	         photos - Allow full access to the photo library.
	         media-library - Allow access to the media library.
	         microphone - Allow access to audio input.
	         motion - Allow access to motion and fitness data.
	         reminders - Allow access to reminders.
	         siri - Allow use of the app with Siri.
```

No `local-network` entry, on any Xcode version we're aware of. This isn't a
retrace gap or a CI config mistake — it isn't exposed through the supported
tool at all.

### Last resort: seed the Simulator's TCC database directly

Only after the checks above point here. Since `simctl` doesn't expose it,
the workaround the wider iOS CI community uses is writing the grant
straight into the booted Simulator's own TCC store, before the app under
test makes its first request:

```sh
UDID=$(xcrun simctl list devices booted -j | \
  jq -r '.devices[][] | select(.state=="Booted") | .udid')
TCC_DB="$HOME/Library/Developer/CoreSimulator/Devices/$UDID/data/Library/TCC/TCC.db"

sqlite3 "$TCC_DB" "INSERT OR REPLACE INTO access
  (service, client, client_type, auth_value, auth_reason, auth_version, flags)
  VALUES ('kTCCServiceLocalNetwork', '<bundle-id-of-the-app-under-test>', 0, 2, 4, 1, 0);"
```

Notes if you adopt this:

- Grant it to the **app under test's** bundle id — the process actually
  making the network calls — not the XCUITest runner's `.xctrunner`
  bundle id.
- The Simulator must already be booted (`xcrun simctl boot <UDID>`) before
  this file exists. Run this step after boot, before `retrace run`/`xcodebuild test`.
- The `access` table's column list is undocumented and has changed across
  macOS/Xcode releases before. The column list above was verified
  against a current Xcode, but run `sqlite3 "$TCC_DB" ".schema access"` on the exact
  runner image you use (`actions/runner-images` pins a specific Xcode per
  `macos-*` label) before trusting it in CI, and re-check when that image
  bumps Xcode.
- This is a widely-used but unofficial technique, not something Apple
  documents or supports. It can stop working on a future Simulator release
  with no notice.
- If it doesn't fix your failure, **revert it** rather than leaving it in
  the job. A no-op hack in CI is worse than none: it looks like a control
  that's already been ruled in.

### If seeding TCC isn't acceptable for your CI policy

There's no Apple-sanctioned bypass we could find. The one alternative worth
considering: run the retrace-backed iOS job on a **persistent** runner
(self-hosted, or a long-lived hosted VM) instead of a fresh ephemeral one
per run, and seed the grant once outside the hot path rather than on every
job. That trades the TCC hack's per-run fragility for runner-fleet
maintenance — worth it only if you're already leaning that way for other
reasons (build cache, licensing, cost).

## See also

- `docs/retrace-ci-example.yml`'s `retrace-ios` job — where these steps slot
  in, right after the Simulator boots and before `retrace run`.
- `.claude/skills/retrace-iterate/SKILL.md`'s "Multiple upstreams" section
  for the dual-stack loopback behavior this doc rules out as the cause, and
  for the `--listen` gotcha that can look like a listener never binding.
