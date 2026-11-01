#!/usr/bin/env python3
"""Fail if required Linux integrations were absent, skipped, or failed."""
import json
import sys

MODULE = "github.com/yosukeno/pogo/"
REQUIRED = {
    "web": ["TestNativeBrowserRealRuntime", "TestNativeBrowserRealPicker"],
    "tools": [
        "TestCommandScopeLinuxPrivateFilesystemAndRuntime",
        "TestCommandScopeLinuxNetworkIsExplicit",
        "TestCommandScopeLinuxCancellationKillsDescendants",
        "TestCommandScopeLinuxDedicatedPTY",
        "TestSandboxedBashCannotReadThisProcessSecrets",
        "TestSandboxedBashCannotWriteOutsideItsGrants",
        "TestSandboxedBashHasNoNetwork",
        "TestSandboxLeavesNoFilesInTheWorkingDirectory",
        "TestSandboxDenyPathsHoldFromInside",
    ],
    "skillruntime": ["TestLocalPlatformIntegration"],
    "skillruntime/core": ["TestNonRootInstallLifecycle"],
}


def check(events):
    expected = {(MODULE + pkg, name) for pkg, names in REQUIRED.items() for name in names}
    outcomes = {}
    invalid = []
    for event in events:
        package, test, action = event.get("Package"), event.get("Test", ""), event.get("Action")
        parent = test.split("/", 1)[0]
        if (package, parent) in expected and action in {"pass", "skip", "fail"}:
            if action != "pass":
                invalid.append(f"{package}/{test}: {action}")
            if test == parent:
                outcomes[(package, test)] = action
    invalid.extend(f"{package}/{test}: no PASS event" for package, test in sorted(expected)
                   if outcomes.get((package, test)) != "pass")
    return invalid


if __name__ == "__main__":
    with open(sys.argv[1]) as stream:
        events = [json.loads(line) for line in stream if line.strip()]
    skipped = sorted({(event.get("Package", ""), event["Test"]) for event in events
                      if event.get("Action") == "skip" and event.get("Test")})
    if skipped:
        print("Skipped tests (not counted as validation):")
        for package, test in skipped:
            print(f"  {package}/{test}")
    errors = check(events)
    if errors:
        print("Required Linux integration gate failed:\n" + "\n".join(errors), file=sys.stderr)
        sys.exit(1)
    print(f"All {sum(map(len, REQUIRED.values()))} required Linux integration tests passed (no skips)")
